// Package proxy 实现透明转发主管线:
// 鉴权 → 提取 → 决策(含限额切换) → 改写 → 流式转发 → 用量统计 → 会话归档。
package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cluster-route/internal/archive"
	"cluster-route/internal/config"
	"cluster-route/internal/rewrite"
	"cluster-route/internal/routing"
	"cluster-route/internal/stats"
	"cluster-route/internal/store"
)

const maxBodyBytes = 32 << 20 // 32MB 请求体上限

// Handler 代理处理器。
type Handler struct {
	mgr        *config.Manager
	engine     *routing.Engine
	recorder   *stats.Recorder
	archiveSvc *archive.Service

	tpl     *http.Transport
	clients sync.Map // providerID -> *clientEntry

	// respLimitBytes 非流式上游响应体上限, 默认 maxBodyBytes; 测试可调小。
	respLimitBytes int64
}

// clientEntry 缓存的客户端及其创建时的超时配置。
// 供应商超时可被用户随时修改, 缓存必须能感知并重建。
type clientEntry struct {
	client     *http.Client
	timeoutSec int
}

// New 创建处理器。
func New(mgr *config.Manager, eng *routing.Engine, rec *stats.Recorder, arch *archive.Service) *Handler {
	return &Handler{
		mgr:            mgr,
		engine:         eng,
		recorder:       rec,
		archiveSvc:     arch,
		respLimitBytes: maxBodyBytes,
		tpl: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// classify 依路径与请求头判定协议格式。
func classify(path string, hdr http.Header) string {
	if strings.HasPrefix(path, "/v1/messages") {
		return "anthropic"
	}
	for _, p := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings",
		"/v1/responses", "/v1/moderations", "/v1/audio", "/v1/images"} {
		if strings.HasPrefix(path, p) {
			return "openai"
		}
	}
	if hdr.Get("anthropic-version") != "" {
		return "anthropic"
	}
	return "openai"
}

func extractKey(r *http.Request) string {
	if h := r.Header.Get("x-api-key"); h != "" {
		return strings.TrimSpace(h)
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// clientFor 按供应商缓存 http.Client(独立响应头超时)。
func (h *Handler) clientFor(p config.Provider) *http.Client {
	for {
		if v, ok := h.clients.Load(p.ID); ok {
			e := v.(*clientEntry)
			if e.timeoutSec == p.TimeoutSec {
				return e.client
			}
			ne := h.newClientEntry(p)
			// 仅当缓存仍是刚才读到的过期条目时才替换, 避免覆盖其他 goroutine 写入的新条目。
			if h.clients.CompareAndSwap(p.ID, e, ne) {
				e.closeIdle()
				return ne.client
			}
			// 竞争失败: 丢弃本次自建的连接池, 回到循环按最新缓存重新判定。
			ne.closeIdle()
			continue
		}
		ne := h.newClientEntry(p)
		if _, loaded := h.clients.LoadOrStore(p.ID, ne); !loaded {
			return ne.client
		}
		// 其他 goroutine 抢先写入: 自建条目不再使用, 关闭其空闲连接后重新判定。
		ne.closeIdle()
	}
}

// newClientEntry 依供应商超时创建新的客户端条目。
func (h *Handler) newClientEntry(p config.Provider) *clientEntry {
	tr := h.tpl.Clone()
	timeout := time.Duration(p.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	tr.ResponseHeaderTimeout = timeout
	return &clientEntry{client: &http.Client{Transport: tr}, timeoutSec: p.TimeoutSec}
}

// closeIdle 释放该条目连接池中的空闲连接。
func (e *clientEntry) closeIdle() {
	if tr, ok := e.client.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

func joinURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") && strings.HasPrefix(path, "/v1/") {
		return b + strings.TrimPrefix(path, "/v1")
	}
	return b + path
}

// writeErr 以对应协议格式输出错误。
func writeErr(w http.ResponseWriter, format string, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var out []byte
	if format == "anthropic" {
		out, _ = json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "cluster_router_error", "message": msg},
		})
	} else {
		out, _ = json.Marshal(map[string]any{
			"error": map[string]any{"message": msg, "type": "cluster_router_error", "code": status},
		})
	}
	w.Write(out)
}

// ServeHTTP 处理 /v1/* 代理请求。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	format := classify(r.URL.Path, r.Header)

	// 鉴权
	routerKey := h.mgr.RouterKey()
	if routerKey == "" || subtle.ConstantTimeCompare([]byte(extractKey(r)), []byte(routerKey)) != 1 {
		writeErr(w, format, http.StatusUnauthorized, "无效的 API Key: 请在 cc-switch/客户端中配置 Cluster Route 的密钥")
		return
	}

	// GET /v1/models 本地应答(模型发现), 不进入转发管线
	if r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/v1/models" {
		h.serveModels(w, format)
		return
	}

	// 读请求体
	var body []byte
	var err error
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		if err != nil {
			writeErr(w, format, http.StatusBadRequest, "读取请求体失败: "+err.Error())
			return
		}
		if len(body) > maxBodyBytes {
			writeErr(w, format, http.StatusRequestEntityTooLarge, "请求体超过 32MB 上限")
			return
		}
	}

	s := h.mgr.Settings()
	var injectText string
	if s.Inject.Enabled && len(body) > 0 {
		injectText = s.Inject.Text
	}

	info := rewrite.Extract(body, format, s.Subagent.Marker)
	dec := h.engine.Decide(info.Model, format, &info)
	if len(dec.Chain) == 0 {
		writeErr(w, format, http.StatusBadGateway,
			"没有可用的转发目标: 请先在 Cluster Route 中添加供应商并配置路由或默认供应商")
		return
	}

	// 尝试链
	var attempts []routing.Attempt
	var resp *http.Response
	var used routing.Target
	for i, t := range dec.Chain {
		attempt := routing.Attempt{Provider: t.Provider.Name, Model: t.Model}
		upBody := body
		if len(body) > 0 {
			b, _, err := rewrite.BuildUpstreamBody(body, &info, t.Model, format,
				s.InjectOpenAIUsage, injectText)
			if err == nil {
				upBody = b
			}
		}
		req, err := h.buildUpstreamRequest(r, t, upBody, format)
		if err != nil {
			attempt.Err = err.Error()
			attempts = append(attempts, attempt)
			continue
		}
		resp, err = h.clientFor(t.Provider).Do(req)
		if err != nil {
			attempt.Err = err.Error()
			attempts = append(attempts, attempt)
			continue
		}
		if h.shouldFailover(resp.StatusCode, &s) && i < len(dec.Chain)-1 {
			attempt.Status = resp.StatusCode
			attempts = append(attempts, attempt)
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			resp = nil
			log.Printf("[proxy] %s %s → %s(%s) 返回 %d, 切换下一目标", r.Method, r.URL.Path,
				t.Provider.Name, t.Model, attempt.Status)
			continue
		}
		attempt.Status = resp.StatusCode
		attempts = append(attempts, attempt)
		used = t
		break
	}

	trace, _ := json.Marshal(attempts)
	if resp == nil {
		lastErr := "全部目标不可用"
		if len(attempts) > 0 {
			lastErr = attempts[len(attempts)-1].Err
		}
		h.record(body, &info, dec, s, trace, "", format, 0, Usage{}, false,
			0, time.Since(start).Milliseconds(), lastErr)
		writeErr(w, format, http.StatusBadGateway, "Cluster Route: "+lastErr)
		return
	}
	defer resp.Body.Close()

	ttfb := time.Since(start).Milliseconds()

	// 上游可能无视 Accept-Encoding: identity 仍返回压缩体。统计解析依赖明文,
	// 因此先解压再走后续管线; 解压失败时保留原响应头与内容, 保证客户端至少能用。
	upBody := decompressBody(resp)

	usage := Usage{}
	var errMsg string
	statusOK := resp.StatusCode >= 200 && resp.StatusCode < 300

	if isSSE(resp) {
		// 流式先写头以尽快开流
		copyRespHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		flusher, _ := w.(http.Flusher)
		parser := NewSSEParser(format)
		buf := make([]byte, 16<<10)
		for {
			n, readErr := upBody.Read(buf)
			if n > 0 {
				parser.FeedChunk(buf[:n])
				if _, werr := w.Write(buf[:n]); werr != nil {
					break // 客户端断开, 停止读取上游
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr != nil {
				break
			}
		}
		usage = parser.Usage()
		errMsg = parser.ErrMsg()
	} else {
		// 非流式先读完再写头: 超限或读取失败时可返回完整错误而非半截内容,
		// 也不会残留上游的 Content-Encoding/Content-Length 头。
		limit := h.respLimitBytes
		if limit <= 0 {
			limit = maxBodyBytes
		}
		rb, rerr := io.ReadAll(io.LimitReader(upBody, limit+1))
		switch {
		case rerr != nil:
			errMsg = "读取上游响应失败: " + rerr.Error()
			h.record(body, &info, dec, s, trace, used.Provider.Name, format, resp.StatusCode,
				Usage{}, false, ttfb, time.Since(start).Milliseconds(), errMsg)
			writeErr(w, format, http.StatusBadGateway, "cluster-route: "+errMsg)
			return
		case int64(len(rb)) > limit:
			errMsg = "上游响应超过 32MB 上限"
			h.record(body, &info, dec, s, trace, used.Provider.Name, format, resp.StatusCode,
				Usage{}, false, ttfb, time.Since(start).Milliseconds(), errMsg)
			writeErr(w, format, http.StatusBadGateway, "cluster-route: "+errMsg)
			return
		}
		usage = ParseNonStream(rb, format)
		copyRespHeaders(w, resp)
		w.Header().Set("Content-Length", strconv.Itoa(len(rb)))
		w.WriteHeader(resp.StatusCode)
		w.Write(rb)
	}

	h.record(body, &info, dec, s, trace, used.Provider.Name, format, resp.StatusCode,
		usage, statusOK && errMsg == "", ttfb, time.Since(start).Milliseconds(), errMsg)
}

// copyRespHeaders 透传上游响应头(跳过逐跳头); Content-Encoding 已在
// decompressBody 中按能否解码决定删除或保留。
func copyRespHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vv := range resp.Header {
		if isHopHeader(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length")
}

// decompressBody 按上游 Content-Encoding 包装响应体, 使读取方拿到明文。
// 成功解压时删除 Content-Encoding/Content-Length(长度由调用方重算);
// 无法解码的编码原样保留并告警, 避免把二进制噪音当成明文解析。
func decompressBody(resp *http.Response) io.Reader {
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var (
		zr  io.Reader
		err error
	)
	switch enc {
	case "", "identity":
		return resp.Body
	case "gzip", "x-gzip":
		rec := &prefixRecorder{r: resp.Body, max: 64 << 10}
		zr, err = gzip.NewReader(rec)
		if err != nil {
			log.Printf("[proxy] 警告: 上游声明 gzip 但解压失败: %v, 原样透传", err)
			return rec.replay()
		}
		rec.disable() // 解压成功, 无需再为回放保留前缀
	case "deflate":
		rec := &prefixRecorder{r: resp.Body, max: 64 << 10}
		zr, err = zlib.NewReader(rec)
		if err != nil {
			// HTTP 的 "deflate" 历史上两种写法都有: RFC 规定 zlib 封装,
			// 但不少服务端实际发送裸 DEFLATE。zlib 头校验失败时按裸流重试。
			// 注意必须用 replay(): zlib.NewReader 可能已从底层读走若干字节,
			// 直接用 resp.Body 会把开头那段丢掉。
			log.Printf("[proxy] 提示: 上游 deflate 非 zlib 封装, 按裸 DEFLATE 重试: %v", err)
			zr = flate.NewReader(rec.replay())
		} else {
			rec.disable()
		}
	default:
		log.Printf("[proxy] 警告: 上游返回不支持的压缩编码 %q, 原样透传(用量统计可能为 0)", enc)
		return resp.Body
	}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	return zr
}

// prefixRecorder 记录底层读取器的前 max 字节。
// gzip/zlib 在头部校验失败前可能已从流中读走若干字节, 直接丢弃解压器会丢数据;
// 记录后回放, 保证原样透传时内容完整。
type prefixRecorder struct {
	r   io.Reader
	buf bytes.Buffer
	max int
}

func (p *prefixRecorder) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if p.max <= 0 {
		return n, err
	}
	if n > 0 && p.buf.Len() < p.max {
		room := p.max - p.buf.Len()
		if n < room {
			room = n
		}
		p.buf.Write(b[:room])
	}
	return n, err
}

// disable 停止记录前缀(解压成功后不再需要回放)。
func (p *prefixRecorder) disable() { p.max = 0; p.buf.Reset() }

// replay 返回已记录前缀与剩余流的拼接。
// 注意这里刻意引用 p.r(原始底层读取器)而非任何解压器: 在 gzip/zlib 头校验
// 失败的场景里, 前缀正是被那个失败的解压器读走的字节, 剩余流应当从底层继续。
func (p *prefixRecorder) replay() io.Reader {
	return io.MultiReader(bytes.NewReader(p.buf.Bytes()), p.r)
}

// shouldFailover 判定是否切换下一目标。
func (h *Handler) shouldFailover(status int, s *config.Settings) bool {
	if status == 429 || status == 402 || status == 403 || status == 408 {
		return true
	}
	if s.Failover.On5xx && status >= 500 {
		return true
	}
	return false
}

func (h *Handler) buildUpstreamRequest(r *http.Request, t routing.Target, body []byte, format string) (*http.Request, error) {
	var base string
	if format == "anthropic" {
		base = t.Provider.AnthropicURL
	} else {
		base = t.Provider.OpenAIURL
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, fmt.Errorf("供应商 %s 未配置 %s 格式端点", t.Provider.Name, format)
	}
	url := joinURL(base, r.URL.Path)
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	var rd io.Reader
	if len(body) > 0 && r.Method != http.MethodGet {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, rd)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 && r.Method != http.MethodGet {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	// 透传关键头。不包含 Accept-Encoding: 本地中转的用量统计依赖明文响应,
	// 且显式设置该头会让 Transport 放弃自动解压, 行为容易产生歧义。
	for _, hk := range []string{"Accept", "Anthropic-Beta", "Anthropic-Version"} {
		if v := r.Header.Get(hk); v != "" {
			req.Header.Set(hk, v)
		}
	}
	// 固定要求明文: 与 Transport 的自动解压互不干扰, 响应侧另有解压兜底。
	req.Header.Set("Accept-Encoding", "identity")
	if format == "anthropic" {
		if req.Header.Get("Anthropic-Version") == "" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
		req.Header.Set("x-api-key", t.Key)
		req.Header.Set("Authorization", "Bearer "+t.Key)
	} else {
		req.Header.Set("Authorization", "Bearer "+t.Key)
	}
	req.Header.Set("User-Agent", "cluster-route/1.0")
	return req, nil
}

func isSSE(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return resp.StatusCode == http.StatusOK && strings.Contains(ct, "text/event-stream")
}

func isHopHeader(k string) bool {
	switch strings.ToLower(k) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"content-length":
		return true
	}
	return false
}

// record 投递统计并触发归档。body 由通道 send 语义保证存活至写盘完成, 无需复制。
func (h *Handler) record(body []byte, info *rewrite.BodyInfo, dec routing.Decision, s config.Settings,
	trace []byte, providerName, format string, status int, u Usage, ok bool,
	ttfbMs, durMs int64, errMsg string) {

	if providerName == "" && len(dec.Chain) > 0 {
		providerName = dec.Chain[0].Provider.Name
	}
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	rec := store.Record{
		Ts:          time.Now().Unix(),
		ProviderID:  providerIDOf(dec, providerName),
		ProviderNam: providerName,
		Model:       info.Model,
		Upstream:    upstreamOf(dec, providerName),
		Channel:     dec.Channel,
		Format:      format,
		Stream:      info.Stream,
		Status:      status,
		OK:          ok,
		Input:       u.Input,
		CacheRead:   u.CacheRead,
		CacheCreate: u.CacheCreate,
		Output:      u.Output,
		DurationMs:  durMs,
		TTFBMs:      ttfbMs,
		Failover:    string(trace),
		Err:         errMsg,
		Project:     info.Project,
		SessionKey:  sessionKeyOf(info),
	}
	h.recorder.Submit(rec)

	if h.archiveSvc != nil && ok && len(dec.Chain) > 0 &&
		dec.Channel != routing.ChannelLightweight && info.Root && info.MsgCount > 0 &&
		archiveWorthy(info, format) {
		h.archiveSvc.Submit(archive.Snapshot{
			Key:      sessionKeyOf(info),
			Project:  info.Project,
			Title:    info.FirstUser,
			Model:    info.Model,
			MsgCount: int64(info.MsgCount),
			Body:     body,
			Ts:       rec.Ts,
		})
	}
}

// archiveWorthy 过滤会话归档: 真实业务请求才有留档价值。
// anthropic(CC)请求必有工具定义; 无工具的小请求多为后台探测任务。
func archiveWorthy(info *rewrite.BodyInfo, format string) bool {
	if format == "anthropic" {
		return info.HasTools
	}
	return info.HasTools || info.MsgCount >= 2
}

// sessionKeyOf 会话键: metadata 会话 ID 优先, 否则项目+首条用户消息指纹。
func sessionKeyOf(info *rewrite.BodyInfo) string {
	if info.SessionKey != "" {
		return info.SessionKey
	}
	sum := sha256.Sum256([]byte(info.Project + "\x00" + info.FirstUser))
	return "fp" + hex.EncodeToString(sum[:8])
}

func providerIDOf(dec routing.Decision, name string) string {
	for _, t := range dec.Chain {
		if t.Provider.Name == name {
			return t.Provider.ID
		}
	}
	if len(dec.Chain) > 0 {
		return dec.Chain[0].Provider.ID
	}
	return ""
}

func upstreamOf(dec routing.Decision, name string) string {
	for _, t := range dec.Chain {
		if t.Provider.Name == name {
			return t.Model
		}
	}
	if len(dec.Chain) > 0 {
		return dec.Chain[0].Model
	}
	return ""
}
