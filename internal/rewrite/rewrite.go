// Package rewrite 从请求体提取路由/统计所需信息, 并做外科手术式改写:
// 模型名替换、OpenAI stream_options 注入、system 提示词注入。
// 不做任何协议转换。
package rewrite

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// BodyInfo 请求体提取结果。
type BodyInfo struct {
	Root         bool   // 是否为合法 JSON object
	Model        string // 客户端请求的模型名(别名)
	Stream       bool
	HasTools     bool
	MsgCount     int
	SystemText   string // 拼接后的 system 文本(截断至 16KB)
	FirstUser    string // 首条用户消息文本(截断, 用于会话指纹/标题)
	SessionKey   string // metadata 中的会话 ID(可空)
	Project      string // 工作目录(可空)
	IsSubagent   bool   // system 不含主标记词
	BodyBytes    int    // 由调用方填充
	AnthropicVer string // anthropic-version 头(透传用)
}

const (
	systemScanCap = 16 * 1024
	firstUserCap  = 400
)

var (
	sessionRe = regexp.MustCompile(`_session_([0-9a-fA-F-]{8,64})`)
	projectRe = regexp.MustCompile(`Working directory:\s*([^\r\n]+)`)
)

// Extract 解析请求体; 解析失败返回 Root=false, 调用方按透传处理。
func Extract(body []byte, format, mainMarker string) BodyInfo {
	var info BodyInfo
	info.BodyBytes = len(body)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return info
	}
	info.Root = true
	info.Model = strField(root, "model")
	if v, ok := root["stream"]; ok {
		_ = json.Unmarshal(v, &info.Stream)
	}
	if raw, ok := root["tools"]; ok && len(raw) > 2 && !bytes.Equal(raw, []byte("null")) {
		info.HasTools = true
	}
	if raw, ok := root["messages"]; ok {
		var msgs []json.RawMessage
		if json.Unmarshal(raw, &msgs) == nil {
			info.MsgCount = len(msgs)
		}
	}

	if format == "anthropic" {
		info.SystemText = anthropicSystem(root)
		info.FirstUser = firstUserText(msgsOf(root))
	} else {
		info.SystemText = openaiSystemText(root)
		info.FirstUser = openaiFirstUser(root)
	}
	info.SystemText = truncateRunes(info.SystemText, systemScanCap)
	info.FirstUser = truncateRunes(info.FirstUser, firstUserCap)

	if raw, ok := root["metadata"]; ok {
		var meta struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(raw, &meta) == nil && meta.UserID != "" {
			if m := sessionRe.FindStringSubmatch(meta.UserID); m != nil {
				info.SessionKey = strings.ToLower(m[1])
			}
		}
	}
	if m := projectRe.FindStringSubmatch(info.SystemText); m != nil {
		info.Project = strings.TrimSpace(m[1])
	}
	mainMarker = strings.TrimSpace(mainMarker)
	if mainMarker != "" {
		info.IsSubagent = info.SystemText != "" && !strings.Contains(info.SystemText, mainMarker)
	}
	return info
}

func msgsOf(root map[string]json.RawMessage) []json.RawMessage {
	raw, ok := root["messages"]
	if !ok {
		return nil
	}
	var msgs []json.RawMessage
	_ = json.Unmarshal(raw, &msgs)
	return msgs
}

// firstUserText 取 anthropic 消息中首条用户消息的文本。
func firstUserText(msgs []json.RawMessage) string {
	for _, m := range msgs {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m, &msg) != nil || msg.Role != "user" {
			continue
		}
		return contentText(msg.Content)
	}
	return ""
}

// anthropicSystem 兼容 string 与 blocks 数组两种形态。
func anthropicSystem(root map[string]json.RawMessage) string {
	raw, ok := root["system"]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if sb.Len() > systemScanCap {
				break
			}
			sb.WriteString(b.Text)
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	return ""
}

// openaiSystemText 取首条 system 消息文本。
func openaiSystemText(root map[string]json.RawMessage) string {
	for _, m := range msgsOf(root) {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m, &msg) != nil {
			continue
		}
		if msg.Role != "system" {
			break // openai 约定 system 在最前
		}
		return contentText(msg.Content)
	}
	return ""
}

func openaiFirstUser(root map[string]json.RawMessage) string {
	for _, m := range msgsOf(root) {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m, &msg) != nil || msg.Role != "user" {
			continue
		}
		return contentText(msg.Content)
	}
	return ""
}

// contentText 兼容 string 与 [{type:text,text}] 两种 content 形态。
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			sb.WriteString(b.Text)
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	return ""
}

// ---- 改写 ----

// BuildUpstreamBody 生成发往指定目标的上游请求体。
// 返回 (新请求体, 是否有改动, 错误)。无改动时原样返回避免重复序列化。
func BuildUpstreamBody(body []byte, info *BodyInfo, targetModel, format string,
	injectUsage bool, injectText string) ([]byte, bool, error) {

	if !info.Root {
		return body, false, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, false, nil
	}
	changed := false

	if targetModel != "" && targetModel != info.Model {
		b, err := json.Marshal(targetModel)
		if err != nil {
			return nil, false, err
		}
		root["model"] = b
		changed = true
	}

	if format == "openai" && info.Stream && injectUsage && !hasIncludeUsage(root) {
		root["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		changed = true
	}

	if injectText != "" {
		switch format {
		case "anthropic":
			if injectAnthropicSystem(root, injectText) {
				changed = true
			}
		case "openai":
			if injectOpenAISystem(root, injectText) {
				changed = true
			}
		}
	}

	if !changed {
		return body, false, nil
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, false, err
	}
	return out, true, nil
}

func hasIncludeUsage(root map[string]json.RawMessage) bool {
	raw, ok := root["stream_options"]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var so struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if json.Unmarshal(raw, &so) != nil {
		return false
	}
	return so.IncludeUsage
}

// injectAnthropicSystem 追加到 system 末尾(保留既有缓存断点, 前缀缓存不失效)。
func injectAnthropicSystem(root map[string]json.RawMessage, text string) bool {
	injectBlk, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	raw, ok := root["system"]
	if !ok || bytes.Equal(raw, []byte("null")) {
		b, _ := json.Marshal([]json.RawMessage{injectBlk})
		root["system"] = b
		return true
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		// string → content block 对象, 保证数组形态合法(Anthropic 要求)
		orig, _ := json.Marshal(map[string]string{"type": "text", "text": s})
		b, _ := json.Marshal([]json.RawMessage{orig, injectBlk})
		root["system"] = b
		return true
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	blocks = append(blocks, injectBlk)
	b, _ := json.Marshal(blocks)
	root["system"] = b
	return true
}

// injectOpenAISystem 追加进首条 system 消息; 无 system 消息时前插一条。
func injectOpenAISystem(root map[string]json.RawMessage, text string) bool {
	raw, ok := root["messages"]
	if !ok {
		return false
	}
	var msgs []json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return false
	}
	if len(msgs) == 0 {
		sys, _ := json.Marshal(map[string]string{"role": "system", "content": text})
		out, _ := json.Marshal([]json.RawMessage{sys})
		root["messages"] = out
		return true
	}
	var first struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msgs[0], &first) != nil || first.Role != "system" {
		sys, _ := json.Marshal(map[string]string{"role": "system", "content": text})
		out := append([]json.RawMessage{sys}, msgs...)
		b, _ := json.Marshal(out)
		root["messages"] = b
		return true
	}
	// 追加到首条 system 内容
	newContent, okc := appendContent(first.Content, text)
	if !okc {
		return false
	}
	patched, _ := json.Marshal(map[string]json.RawMessage{
		"role":    json.RawMessage(`"system"`),
		"content": newContent,
	})
	// 保留首条消息里的其他字段
	var firstMap map[string]json.RawMessage
	if json.Unmarshal(msgs[0], &firstMap) == nil {
		firstMap["content"] = newContent
		patched, _ = json.Marshal(firstMap)
	}
	msgs[0] = patched
	b, _ := json.Marshal(msgs)
	root["messages"] = b
	return true
}

func appendContent(raw json.RawMessage, text string) (json.RawMessage, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		b, _ := json.Marshal(text)
		return b, true
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, false
		}
		b, _ := json.Marshal(s + "\n\n" + text)
		return b, true
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, false
	}
	tb, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	var arr []json.RawMessage
	for _, blk := range blocks {
		b, _ := json.Marshal(blk)
		arr = append(arr, b)
	}
	arr = append(arr, tb)
	b, _ := json.Marshal(arr)
	return b, true
}

func strField(root map[string]json.RawMessage, key string) string {
	raw, ok := root[key]
	if !ok || len(raw) < 2 || raw[0] != '"' {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func truncateRunes(s string, capBytes int) string {
	if len(s) <= capBytes {
		return s
	}
	cut := capBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // 不截断 UTF-8 中间
		cut--
	}
	return s[:cut]
}
