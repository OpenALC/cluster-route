// Package proxy 实现透明转发主管线:
// 鉴权 → 提取 → 决策(含限额切换) → 改写 → 流式转发 → 用量统计 → 会话归档。
package proxy

import (
	"bytes"
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

	"cluster-router/internal/archive"
	"cluster-router/internal/config"
	"cluster-router/internal/routing"
	"cluster-router/internal/rewrite"
	"cluster-router/internal/stats"
	"cluster-router/internal/store"
)

const maxBodyBytes = 32 << 20 // 32MB 请求体上限

// Handler 代理处理器。
type Handler struct {
	mgr        *config.Manager
	engine     *routing.Engine
	recorder   *stats.Recorder
	archiveSvc *archive.Service

	tpl     *http.Transport
	clients sync.Map // providerID -> *http.Client
}

// New 创建处理器。
func New(mgr *config.Manager, eng *routing.Engine, rec *stats.Recorder, arch *archive.Service) *Handler {
	return &Handler{
		mgr:        mgr,
		engine:     eng,
		recorder:   rec,
		archiveSvc: arch,
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
	if c, ok := h.clients.Load(p.ID); ok {
		return c.(*http.Client)
	}
	tr := h.tpl.Clone()
	timeout := time.Duration(p.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	tr.ResponseHeaderTimeout = timeout
	c := &http.Client{Transport: tr}
	actual, _ := h.clients.LoadOrStore(p.ID, c)
	return actual.(*http.Client)
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
		writeErr(w, format, http.StatusUnauthorized, "无效的 API Key: 请在 cc-switch/客户端中配置 cluster-router 的密钥")
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
			"没有可用的转发目标: 请先在 cluster-router 中添加供应商并配置路由或默认供应商")
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
		writeErr(w, format, http.StatusBadGateway, "cluster-router: "+lastErr)
		return
	}
	defer resp.Body.Close()

	ttfb := time.Since(start).Milliseconds()

	// 透传响应头
	for k, vv := range resp.Header {
		if isHopHeader(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length")

	usage := Usage{}
	var errMsg string
	statusOK := resp.StatusCode >= 200 && resp.StatusCode < 300

	if isSSE(resp) {
		w.WriteHeader(resp.StatusCode)
		flusher, _ := w.(http.Flusher)
		parser := NewSSEParser(format)
		buf := make([]byte, 16<<10)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				parser.FeedChunk(buf[:n])
				if _, werr := w.Write(buf[:n]); werr != nil {
					break // 客户端断开
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
		rb, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if rerr == nil {
			usage = ParseNonStream(rb, format)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(rb)))
		w.WriteHeader(resp.StatusCode)
		w.Write(rb)
	}

	h.record(body, &info, dec, s, trace, used.Provider.Name, format, resp.StatusCode,
		usage, statusOK && errMsg == "", ttfb, time.Since(start).Milliseconds(), errMsg)
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
	// 透传关键头
	for _, hk := range []string{"Accept", "Accept-Encoding", "Anthropic-Beta", "Anthropic-Version"} {
		if v := r.Header.Get(hk); v != "" {
			req.Header.Set(hk, v)
		}
	}
	if format == "anthropic" {
		if req.Header.Get("Anthropic-Version") == "" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
		req.Header.Set("x-api-key", t.Key)
		req.Header.Set("Authorization", "Bearer "+t.Key)
	} else {
		req.Header.Set("Authorization", "Bearer "+t.Key)
	}
	req.Header.Set("User-Agent", "cluster-router/1.0")
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
