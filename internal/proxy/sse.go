// Package proxy 的用量解析: 流式按行扫描 SSE data 帧, 非流式解析顶层 usage。
// 只保留含 "usage" 的行, 行缓冲封顶, 内存占用与响应时长无关。
package proxy

import (
	"bytes"
	"encoding/json"
)

// Usage 四类 token 用量。
type Usage struct {
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
}

// anthropic usage 结构(兼容旧整型与新 cache_creation 对象两种形态)。
type aUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (u *aUsage) cacheCreateTotal() int64 {
	if u.CacheCreationInputTokens > 0 {
		return u.CacheCreationInputTokens
	}
	if u.CacheCreation != nil {
		return u.CacheCreation.Ephemeral5m + u.CacheCreation.Ephemeral1h
	}
	return 0
}

// openai usage 结构。chat completions 用 prompt/completion_tokens;
// responses API 用 input/output_tokens。两者的总量都包含 cached_tokens。
type oUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"` // 部分中转的 anthropic 风格字段, 不包含在总量里
}

// applyOpenAIUsage 把一帧 OpenAI/Responses 用量写入 dst。
// 官方 cached_tokens 已含在 prompt_tokens/input_tokens 中, 必须从输入里扣掉,
// 否则计价会按输入单价再乘一遍。cache_read_input_tokens 是中转附加字段, 语义与
// Anthropic 相同(不含在总量里), 只记缓存读, 不从输入扣。
func applyOpenAIUsage(dst *Usage, u *oUsage) {
	if u == nil {
		return
	}
	in, hasIn := u.PromptTokens, u.PromptTokens > 0
	if !hasIn && u.InputTokens > 0 {
		in, hasIn = u.InputTokens, true
	}
	out, hasOut := u.CompletionTokens, u.CompletionTokens > 0
	if !hasOut && u.OutputTokens > 0 {
		out, hasOut = u.OutputTokens, true
	}
	included := int64(0)
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		included = u.PromptTokensDetails.CachedTokens
	} else if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens > 0 {
		included = u.InputTokensDetails.CachedTokens
	}
	if included > in {
		included = in
	}
	if hasIn {
		dst.Input = in - included
	}
	if hasOut {
		dst.Output = out
	}
	if included > 0 {
		dst.CacheRead = included
	}
	if u.CacheReadInputTokens > 0 {
		dst.CacheRead = u.CacheReadInputTokens
	}
}

// SSEParser 逐块喂入响应字节, 提取用量。
type SSEParser struct {
	format string
	u      Usage
	// 内部行缓冲
	line   []byte
	inLine bool
	over   bool // 当前行超限, 丢弃直到换行
	errMsg string
}

// NewSSEParser 创建解析器(format: anthropic|openai)。
func NewSSEParser(format string) *SSEParser {
	return &SSEParser{format: format}
}

const maxLineLen = 1 << 20

// ErrMsg 流中捕获的 error 事件信息(可空)。
func (p *SSEParser) ErrMsg() string { return p.errMsg }

// Usage 返回已提取的用量。
func (p *SSEParser) Usage() Usage { return p.u }

// FeedChunk 喂入一段响应字节(不复制)。
func (p *SSEParser) FeedChunk(chunk []byte) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			p.feedPartial(chunk)
			return
		}
		p.feedPartial(chunk[:i])
		p.finishLine()
		chunk = chunk[i+1:]
	}
}

func (p *SSEParser) feedPartial(b []byte) {
	if p.over {
		return
	}
	if len(p.line)+len(b) > maxLineLen {
		p.over = true
		p.line = p.line[:0]
		return
	}
	p.line = append(p.line, b...)
	p.inLine = true
}

func (p *SSEParser) finishLine() {
	if p.over {
		p.over = false
		p.line = p.line[:0]
		p.inLine = false
		return
	}
	if p.inLine {
		p.feedLine(p.line)
		p.line = p.line[:0]
		p.inLine = false
	}
}

// Flush 处理流结束时尚未换行的最后一帧。不调用会丢掉末帧用量。
func (p *SSEParser) Flush() {
	p.finishLine()
}

// feedLine 处理一行完整 SSE 文本(不含换行)。
func (p *SSEParser) feedLine(line []byte) {
	payload, ok := sseData(line)
	if !ok {
		return
	}
	if p.errMsg == "" {
		p.captureStreamErr(payload)
	}
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return
	}
	if p.format == "anthropic" {
		p.parseAnthropic(payload)
	} else {
		p.parseOpenAI(payload)
	}
}

// captureStreamErr 从 data 负载识别错误事件。整行带 data: 前缀, 不能直接 Unmarshal。
// Anthropic 是 {"type":"error","error":{...}}; OpenAI 是 {"error":{...}}, 没有 type:error。
func (p *SSEParser) captureStreamErr(payload []byte) {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return
	}
	var ev struct {
		Type  string `json:"type"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &ev) != nil || ev.Error == nil || ev.Error.Message == "" {
		return
	}
	if ev.Type == "error" || ev.Type == "" || ev.Type == "response.failed" {
		p.errMsg = ev.Error.Message
	}
}

// sseData 提取 data: 前缀后的负载; 非 data 行返回 false。
func sseData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	return bytes.TrimSpace(line[len("data:"):]), true
}

func (p *SSEParser) parseAnthropic(payload []byte) {
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *aUsage `json:"usage"`
		} `json:"message"`
		Delta *struct {
			Usage *aUsage `json:"usage"`
		} `json:"delta"`
		Usage *aUsage `json:"usage"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	mergeA := func(u *aUsage) {
		if u == nil {
			return
		}
		if u.InputTokens > 0 {
			p.u.Input = u.InputTokens
		}
		if u.CacheReadInputTokens > 0 {
			p.u.CacheRead = u.CacheReadInputTokens
		}
		if cc := u.cacheCreateTotal(); cc > 0 {
			p.u.CacheCreate = cc
		}
		if u.OutputTokens > p.u.Output {
			p.u.Output = u.OutputTokens
		}
	}
	if ev.Message != nil {
		mergeA(ev.Message.Usage)
	}
	if ev.Delta != nil {
		mergeA(ev.Delta.Usage)
	}
	if ev.Usage != nil {
		mergeA(ev.Usage)
	}
}

func (p *SSEParser) parseOpenAI(payload []byte) {
	var ev struct {
		Usage    *oUsage `json:"usage"`
		Response *struct {
			Usage *oUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	applyOpenAIUsage(&p.u, ev.Usage)
	if ev.Response != nil {
		applyOpenAIUsage(&p.u, ev.Response.Usage)
	}
}

// ParseNonStream 解析非流式响应体的用量。
func ParseNonStream(body []byte, format string) Usage {
	var u Usage
	if format == "anthropic" {
		// count_tokens 等端点直接返回顶层 input_tokens
		var top struct {
			InputTokens int64 `json:"input_tokens"`
		}
		if json.Unmarshal(body, &top) == nil && top.InputTokens > 0 {
			u.Input = top.InputTokens
			return u
		}
		var resp struct {
			Usage *aUsage `json:"usage"`
		}
		if json.Unmarshal(body, &resp) == nil && resp.Usage != nil {
			u.Input = resp.Usage.InputTokens
			u.Output = resp.Usage.OutputTokens
			u.CacheRead = resp.Usage.CacheReadInputTokens
			u.CacheCreate = resp.Usage.cacheCreateTotal()
		}
		return u
	}
	var resp struct {
		Usage    *oUsage `json:"usage"`
		Response *struct {
			Usage *oUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return u
	}
	if resp.Usage != nil {
		applyOpenAIUsage(&u, resp.Usage)
		return u
	}
	if resp.Response != nil {
		applyOpenAIUsage(&u, resp.Response.Usage)
	}
	return u
}
