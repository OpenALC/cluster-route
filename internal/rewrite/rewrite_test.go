// Package rewrite_test 覆盖 internal/rewrite 的两条主路径:
//  1. Extract: anthropic/openai 的 system、tools、messages、metadata 会话 ID、
//     工作目录、子 agent 判定与非法 JSON 容错;
//  2. BuildUpstreamBody: 模型名改写、OpenAI 流式 usage 注入、anthropic/openai
//     system 提示词注入, 以及"无改动原样返回"。
//
// 所有断言均基于 encoding/json 反序列化后的结构, 不依赖键顺序。
package rewrite_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"cluster-route/internal/rewrite"
)

const mainMarker = "You are Claude Code"

// mustExtract 解析请求体; 调用方清一色是合法 JSON 时可直接用。
func mustExtract(t *testing.T, body, format string) rewrite.BodyInfo {
	t.Helper()
	return rewrite.Extract([]byte(body), format, mainMarker)
}

// jsonQuote 把字符串编码为 JSON 字面量, 避免手写转义。
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// JSON 去除多行原始 JSON 里的空白换行, 便于用原始字符串书写用例。
func JSON(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		panic(err)
	}
	return buf.String()
}

// decodeObj 反序列化为通用对象, 供结构断言。
func decodeObj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("反序列化失败: %v\n原文: %s", err, b)
	}
	return m
}

// messagesOf 取出 messages 数组。
func messagesOf(t *testing.T, root map[string]any) []any {
	t.Helper()
	v, ok := root["messages"].([]any)
	if !ok {
		t.Fatalf("messages 不是数组: %#v", root["messages"])
	}
	return v
}

// textBlocks 将 anthropic system 数组转为文本列表。
func textBlocks(t *testing.T, v any) []string {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("期望数组, got %#v", v)
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		blk, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("block 不是对象: %#v", it)
		}
		out = append(out, blk["text"].(string))
	}
	return out
}

// assertUnchanged 断言无改动路径: 返回同一内容且 changed=false。
func assertUnchanged(t *testing.T, orig, got []byte, changed bool) {
	t.Helper()
	if changed {
		t.Fatalf("changed = true, want false")
	}
	if !bytes.Equal(orig, got) {
		t.Fatalf("请求体被修改:\n原: %s\n新: %s", orig, got)
	}
}

// TestExtractAnthropicSystem anthropic 的 system 支持 string 与 blocks 数组两种形态。
func TestExtractAnthropicSystem(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "string 形态",
			body: `{"model":"m","system":"You are Claude Code","messages":[]}`,
			want: "You are Claude Code",
		},
		{
			name: "blocks 数组形态逐块拼接",
			body: `{"model":"m","system":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}],"messages":[]}`,
			want: "part1\npart2\n",
		},
		{
			name: "system 缺失",
			body: `{"model":"m","messages":[]}`,
			want: "",
		},
		{
			name: "system 为 null",
			body: `{"model":"m","system":null,"messages":[]}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := mustExtract(t, tc.body, "anthropic")
			if info.SystemText != tc.want {
				t.Fatalf("SystemText = %q, want %q", info.SystemText, tc.want)
			}
		})
	}
}

// TestExtractOpenAISystem openai 取首条 system 消息, 且仅认最前面的 system。
func TestExtractOpenAISystem(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "首条 system string",
			body: `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`,
			want: "sys",
		},
		{
			name: "首条 system blocks",
			body: `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
			want: "a\nb\n",
		},
		{
			name: "无 system",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			want: "",
		},
		{
			name: "system 不在首位则忽略",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":"late"}]}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := mustExtract(t, tc.body, "openai")
			if info.SystemText != tc.want {
				t.Fatalf("SystemText = %q, want %q", info.SystemText, tc.want)
			}
		})
	}
}

// TestExtractTools tools 存在/不存在/null/空数组的判定。
func TestExtractTools(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"存在工具", `{"model":"m","tools":[{"name":"t"}],"messages":[]}`, true},
		{"不存在", `{"model":"m","messages":[]}`, false},
		{"为 null", `{"model":"m","tools":null,"messages":[]}`, false},
		{"空数组", `{"model":"m","tools":[],"messages":[]}`, false},
		// 语义等价: 带空白的空数组也必须判为无工具(曾有 len(raw)>2 的字节级误判)
		{"带空白的空数组", `{"model":"m","tools":[ ],"messages":[]}`, false},
		{"非数组形态", `{"model":"m","tools":{},"messages":[]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustExtract(t, tc.body, "anthropic").HasTools; got != tc.want {
				t.Fatalf("HasTools = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtractMessagesCount 消息计数与其它基础字段。
func TestExtractMessagesCount(t *testing.T) {
	body := `{"model":"claude-x","stream":true,"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":"two"},
		{"role":"user","content":"three"}]}`
	info := mustExtract(t, body, "anthropic")
	if !info.Root {
		t.Fatal("Root = false, want true")
	}
	if info.Model != "claude-x" {
		t.Fatalf("Model = %q, want claude-x", info.Model)
	}
	if !info.Stream {
		t.Fatal("Stream = false, want true")
	}
	if info.MsgCount != 3 {
		t.Fatalf("MsgCount = %d, want 3", info.MsgCount)
	}
	if info.BodyBytes != len(body) {
		t.Fatalf("BodyBytes = %d, want %d", info.BodyBytes, len(body))
	}
	if info.FirstUser != "one" {
		t.Fatalf("FirstUser = %q, want one", info.FirstUser)
	}
}

// TestExtractSessionKey 从 metadata.user_id 的 _session_<uuid> 提取并小写化。
func TestExtractSessionKey(t *testing.T) {
	cases := []struct {
		name   string
		userID string
		want   string
	}{
		{"标准格式", "user_abc_session_ABC123DE-0000-1111-2222-333344445555", "abc123de-0000-1111-2222-333344445555"},
		{"无前缀", "_session_deadbeef", "deadbeef"},
		{"无匹配", "plain-user-id", ""},
		{"过短不匹配", "_session_abc", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"m","metadata":{"user_id":` + jsonQuote(tc.userID) + `},"messages":[]}`
			info := mustExtract(t, body, "anthropic")
			if info.SessionKey != tc.want {
				t.Fatalf("SessionKey = %q, want %q", info.SessionKey, tc.want)
			}
		})
	}
}

// TestExtractProject 从 system 文本提取 Working directory。
func TestExtractProject(t *testing.T) {
	body := JSON(`{
		"model":"m",
		"system":"You are Claude Code\nWorking directory: /home/u/proj\nmore",
		"messages":[]
	}`)
	info := mustExtract(t, body, "anthropic")
	if info.Project != "/home/u/proj" {
		t.Fatalf("Project = %q, want /home/u/proj", info.Project)
	}
}

// TestExtractIsSubagent 子 agent 判定: system 非空且不含主标记词。
func TestExtractIsSubagent(t *testing.T) {
	cases := []struct {
		name   string
		system string
		want   bool
	}{
		{"含标记 → 主 agent", "You are Claude Code, an assistant", false},
		{"不含标记 → 子 agent", "You are a helpful reviewer", true},
		{"system 为空 → 不判定为子 agent", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			if tc.system == "" {
				body = `{"model":"m","messages":[]}`
			} else {
				body = `{"model":"m","system":` + jsonQuote(tc.system) + `,"messages":[]}`
			}
			if got := mustExtract(t, body, "anthropic").IsSubagent; got != tc.want {
				t.Fatalf("IsSubagent = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtractInvalidJSON 非法 JSON 与非对象根节点 → Root=false 且各字段归零。
func TestExtractInvalidJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"语法错误", `{"model":`},
		{"数组根", `[1,2,3]`},
		{"null 根", `null`},
		{"空串", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := rewrite.Extract([]byte(tc.body), "anthropic", mainMarker)
			if info.Root {
				t.Fatal("Root = true, want false")
			}
			if info.Model != "" || info.MsgCount != 0 || info.HasTools {
				t.Fatalf("非法 JSON 的字段应归零, got %+v", info)
			}
			if info.BodyBytes != len(tc.body) {
				t.Fatalf("BodyBytes = %d, want %d", info.BodyBytes, len(tc.body))
			}
		})
	}
}

// ---- BuildUpstreamBody ----

// ptrInfo 解析请求体并返回指针, 便于行内调用。
func ptrInfo(t *testing.T, body, format string) *rewrite.BodyInfo {
	t.Helper()
	info := rewrite.Extract([]byte(body), format, mainMarker)
	return &info
}

// build 以已解析的 BodyInfo 调用改写并返回 (结果, changed)。
func build(t *testing.T, body string, info *rewrite.BodyInfo, targetModel, format string,
	injectUsage bool, injectText string) ([]byte, bool) {
	t.Helper()
	b, changed, err := rewrite.BuildUpstreamBody(
		[]byte(body), info, targetModel, format, injectUsage, injectText)
	if err != nil {
		t.Fatalf("BuildUpstreamBody: %v", err)
	}
	return b, changed
}

// TestBuildModelRewrite 目标模型与请求不同时改写 model 字段。
func TestBuildModelRewrite(t *testing.T) {
	body := `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`
	info := mustExtract(t, body, "anthropic")

	out, changed := build(t, body, &info, "upstream-real", "anthropic", false, "")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	if got := root["model"]; got != "upstream-real" {
		t.Fatalf("model = %v, want upstream-real", got)
	}
}

// TestBuildModelNoop 目标模型为空或与请求相同 → 不做改动。
func TestBuildModelNoop(t *testing.T) {
	body := `{"model":"same","messages":[]}`
	info := mustExtract(t, body, "anthropic")

	t.Run("目标为空", func(t *testing.T) {
		out, changed := build(t, body, &info, "", "anthropic", false, "")
		assertUnchanged(t, []byte(body), out, changed)
	})
	t.Run("目标与请求相同", func(t *testing.T) {
		out, changed := build(t, body, &info, "same", "anthropic", false, "")
		assertUnchanged(t, []byte(body), out, changed)
	})
}

// TestBuildOpenAIStreamUsage OpenAI + stream + injectUsage 时注入 stream_options.include_usage。
func TestBuildOpenAIStreamUsage(t *testing.T) {
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	info := mustExtract(t, body, "openai")

	out, changed := build(t, body, &info, "", "openai", true, "")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	so, ok := root["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options 缺失或类型错误: %#v", root["stream_options"])
	}
	if so["include_usage"] != true {
		t.Fatalf("include_usage = %v, want true", so["include_usage"])
	}
}

// TestBuildOpenAIStreamUsageNotInjected 注入条件不满足时不写 stream_options。
func TestBuildOpenAIStreamUsageNotInjected(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		format      string
		injectUsage bool
	}{
		{"非流式", `{"model":"m","stream":false,"messages":[]}`, "openai", true},
		{"injectUsage 关闭", `{"model":"m","stream":true,"messages":[]}`, "openai", false},
		{"anthropic 格式", `{"model":"m","stream":true,"messages":[]}`, "anthropic", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := mustExtract(t, tc.body, tc.format)
			out, changed := build(t, tc.body, &info, "", tc.format, tc.injectUsage, "")
			assertUnchanged(t, []byte(tc.body), out, changed)
		})
	}
}

// TestBuildOpenAIStreamUsagePreserved 已存在 include_usage=true 时不覆盖。
func TestBuildOpenAIStreamUsagePreserved(t *testing.T) {
	body := `{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[]}`
	info := mustExtract(t, body, "openai")
	out, changed := build(t, body, &info, "", "openai", true, "")
	assertUnchanged(t, []byte(body), out, changed)
}

// TestBuildOpenAIStreamUsageOverridesFalse 已存在 include_usage=false 时补开,
// 且必须保留 stream_options 中的其它字段(不能整体覆盖)。
func TestBuildOpenAIStreamUsageOverridesFalse(t *testing.T) {
	body := `{"model":"m","stream":true,"stream_options":{"include_usage":false,"x":1},"messages":[]}`
	info := mustExtract(t, body, "openai")
	out, changed := build(t, body, &info, "", "openai", true, "")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	so, ok := root["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options 类型错误: %#v", root["stream_options"])
	}
	if so["include_usage"] != true {
		t.Fatalf("include_usage = %v, want true", so["include_usage"])
	}
	if so["x"] != float64(1) {
		t.Fatalf("stream_options.x = %v, want 1 (其它字段应保留)", so["x"])
	}
}

// TestBuildOpenAIStreamUsageMalformedOptions 非对象的 stream_options 可被安全重建。
func TestBuildOpenAIStreamUsageMalformedOptions(t *testing.T) {
	body := `{"model":"m","stream":true,"stream_options":"oops","messages":[]}`
	info := mustExtract(t, body, "openai")
	out, changed := build(t, body, &info, "", "openai", true, "")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	so, ok := root["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options 类型错误: %#v", root["stream_options"])
	}
	if so["include_usage"] != true {
		t.Fatalf("include_usage = %v, want true", so["include_usage"])
	}
}

// TestBuildAnthropicSystemNoSystem 无 system / system 为 null 时新建 blocks 数组。
func TestBuildAnthropicSystemNoSystem(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","system":null,"messages":[]}`,
	} {
		out, changed := build(t, body, ptrInfo(t, body, "anthropic"), "", "anthropic", false, "INJECT")
		if !changed {
			t.Fatalf("changed = false, want true (body %s)", body)
		}
		root := decodeObj(t, out)
		blocks := textBlocks(t, root["system"])
		if len(blocks) != 1 || blocks[0] != "INJECT" {
			t.Fatalf("system blocks = %v, want [INJECT]", blocks)
		}
	}
}

// TestBuildAnthropicSystemString 字符串 system 转换为内容块并追加注入文本。
func TestBuildAnthropicSystemString(t *testing.T) {
	body := `{"model":"m","system":"orig","messages":[]}`
	out, changed := build(t, body, ptrInfo(t, body, "anthropic"), "", "anthropic", false, "INJECT")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	blocks := textBlocks(t, root["system"])
	if len(blocks) != 2 || blocks[0] != "orig" || blocks[1] != "INJECT" {
		t.Fatalf("system blocks = %v, want [orig INJECT]", blocks)
	}
}

// TestBuildAnthropicSystemBlocks 数组 system 追加到末尾, 保留原有块。
func TestBuildAnthropicSystemBlocks(t *testing.T) {
	body := `{"model":"m","system":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"messages":[]}`
	out, changed := build(t, body, ptrInfo(t, body, "anthropic"), "", "anthropic", false, "INJECT")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	blocks := textBlocks(t, root["system"])
	want := []string{"a", "b", "INJECT"}
	if len(blocks) != len(want) {
		t.Fatalf("system blocks = %v, want %v", blocks, want)
	}
	for i := range want {
		if blocks[i] != want[i] {
			t.Fatalf("system blocks = %v, want %v", blocks, want)
		}
	}
}

// TestBuildInjectEmptyNoChange injectText 为空且无其他改动 → 原样返回。
func TestBuildInjectEmptyNoChange(t *testing.T) {
	body := `{"model":"m","system":"orig","messages":[{"role":"user","content":"hi"}]}`
	out, changed := build(t, body, ptrInfo(t, body, "anthropic"), "", "anthropic", false, "")
	assertUnchanged(t, []byte(body), out, changed)
}

// TestBuildInvalidJSON 非法 JSON 或 Root=false 时原样返回且不报错。
func TestBuildInvalidJSON(t *testing.T) {
	body := `{"model":`
	info := rewrite.Extract([]byte(body), "anthropic", mainMarker)
	out, changed, err := rewrite.BuildUpstreamBody(
		[]byte(body), &info, "x", "anthropic", true, "INJECT")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	assertUnchanged(t, []byte(body), out, changed)
}

// TestBuildOpenAISystemExistingString 有首条 system(string content) 时追加注入文本。
func TestBuildOpenAISystemExistingString(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":"orig"},{"role":"user","content":"hi"}]}`
	out, changed := build(t, body, ptrInfo(t, body, "openai"), "", "openai", false, "INJECT")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	msgs := messagesOf(t, root)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("首条角色 = %v, want system", first["role"])
	}
	if got := first["content"]; got != "orig\n\nINJECT" {
		t.Fatalf("content = %q, want %q", got, "orig\n\nINJECT")
	}
}

// TestBuildOpenAISystemExistingBlocks 首条 system 的 content 为 blocks 数组时追加文本块。
func TestBuildOpenAISystemExistingBlocks(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"a"}]},{"role":"user","content":"hi"}]}`
	out, changed := build(t, body, ptrInfo(t, body, "openai"), "", "openai", false, "INJECT")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	msgs := messagesOf(t, root)
	first := msgs[0].(map[string]any)
	blocks := textBlocks(t, first["content"])
	if len(blocks) != 2 || blocks[0] != "a" || blocks[1] != "INJECT" {
		t.Fatalf("content blocks = %v, want [a INJECT]", blocks)
	}
}

// TestBuildOpenAISystemNoSystem 无 system 消息时前插一条。
func TestBuildOpenAISystemNoSystem(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"有用户消息", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"messages 为空数组", `{"model":"m","messages":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := build(t, tc.body, ptrInfo(t, tc.body, "openai"), "", "openai", false, "INJECT")
			if !changed {
				t.Fatal("changed = false, want true")
			}
			root := decodeObj(t, out)
			msgs := messagesOf(t, root)
			first := msgs[0].(map[string]any)
			if first["role"] != "system" || first["content"] != "INJECT" {
				t.Fatalf("首条 = %#v, want system/INJECT", first)
			}
		})
	}
}

// TestBuildOpenAISystemNoMessagesField messages 缺失时注入返回无改动(实现约定)。
func TestBuildOpenAISystemNoMessagesField(t *testing.T) {
	body := `{"model":"m","input":"x"}`
	out, changed := build(t, body, ptrInfo(t, body, "openai"), "", "openai", false, "INJECT")
	assertUnchanged(t, []byte(body), out, changed)
}

// TestBuildOpenAISystemPreservesExtraFields 追加内容时保留首条 system 的其它字段。
func TestBuildOpenAISystemPreservesExtraFields(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":"orig","name":"sys"},{"role":"user","content":"hi"}]}`
	out, changed := build(t, body, ptrInfo(t, body, "openai"), "", "openai", false, "INJECT")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	first := messagesOf(t, root)[0].(map[string]any)
	if first["name"] != "sys" {
		t.Fatalf("name = %v, want sys", first["name"])
	}
	if first["content"] != "orig\n\nINJECT" {
		t.Fatalf("content = %q, want orig\\n\\nINJECT", first["content"])
	}
}

// TestBuildCombined 模型改写、usage 注入与 system 注入可同时生效。
func TestBuildCombined(t *testing.T) {
	body := `{"model":"alias","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	out, changed := build(t, body, ptrInfo(t, body, "openai"), "real", "openai", true, "INJECT")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	root := decodeObj(t, out)
	if root["model"] != "real" {
		t.Fatalf("model = %v, want real", root["model"])
	}
	if so := root["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Fatalf("include_usage = %v, want true", so["include_usage"])
	}
	first := messagesOf(t, root)[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "INJECT" {
		t.Fatalf("首条 = %#v, want system/INJECT", first)
	}
}
