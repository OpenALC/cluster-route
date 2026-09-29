// 上游探测: 供应商连通性测试与模型列表拉取(免费接口, 不产生 token 计费)。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cluster-route/internal/config"
)

// upstreamClient 独立短超时客户端, 不与转发共享连接池。
var upstreamClient = &http.Client{Timeout: 20 * time.Second}

// fetchModelList 拉取供应商模型列表, 统一解析 {"data":[{"id":...}]}。
func fetchModelList(ctx context.Context, p config.Provider, key, format string) ([]string, error) {
	var base string
	if format == "anthropic" {
		base = p.AnthropicURL
	} else {
		base = p.OpenAIURL
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, fmt.Errorf("未配置 %s 格式端点", format)
	}
	url := joinUpstream(base, "/v1/models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if format == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Anthropic-Version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("响应解析失败(可能不支持 /v1/models): %w", err)
	}
	var out []string
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("模型列表为空(可能不支持 /v1/models, 请手动添加路由)")
	}
	return out, nil
}

func joinUpstream(base, path string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") && strings.HasPrefix(path, "/v1/") {
		return b + strings.TrimPrefix(path, "/v1")
	}
	return b + path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
