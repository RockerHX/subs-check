package check

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/beck-8/subs-check/config"
)

// ExternalFilterRequest is the JSON payload sent to an external filter service.
// The service should respond with ExternalFilterResponse.
type ExternalFilterRequest struct {
	Name     string         `json:"name"`
	Proxy    map[string]any `json:"proxy"`
	IP       string         `json:"ip,omitempty"`
	Country  string         `json:"country,omitempty"`
	IPRisk   string         `json:"ip_risk,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ExternalFilterResponse is returned by the external filter service.
type ExternalFilterResponse struct {
	Keep   bool     `json:"keep"`
	Tags   []string `json:"tags,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

type ExternalFilterClient struct {
	url    string
	client *http.Client
}

func NewExternalFilterClient() *ExternalFilterClient {
	url := strings.TrimSpace(config.GlobalConfig.ExternalFilterURL)
	if url == "" {
		return nil
	}
	timeout := config.GlobalConfig.ExternalFilterTimeout
	if timeout <= 0 {
		timeout = 10
	}
	return &ExternalFilterClient{
		url: url,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
	}
}

func (c *ExternalFilterClient) Apply(ctx context.Context, r Result) (Result, bool) {
	if c == nil {
		return r, true
	}
	payload := ExternalFilterRequest{
		Name:    RenderName(r, false),
		Proxy:   r.Proxy,
		IP:      r.IP,
		Country: r.Country,
		IPRisk:  r.IPRisk,
		Metadata: map[string]any{
			"youtube":    r.Youtube,
			"google":     r.Google,
			"cloudflare": r.Cloudflare,
			"disney":     r.Disney,
			"gemini":     r.Gemini,
			"tiktok":     r.TikTok,
			"claude":     r.Claude,
			"spotify":    r.Spotify,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn(fmt.Sprintf("外部过滤请求序列化失败: %v", err))
		return r, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		slog.Warn(fmt.Sprintf("外部过滤请求创建失败: %v", err))
		return r, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "subs-check external-filter")

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn(fmt.Sprintf("外部过滤请求失败: %v", err))
		return r, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn(fmt.Sprintf("外部过滤返回非200状态码: %d", resp.StatusCode))
		return r, false
	}
	var out ExternalFilterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		slog.Warn(fmt.Sprintf("外部过滤响应解析失败: %v", err))
		return r, false
	}
	if !out.Keep {
		return r, false
	}
	r.ExtraTags = append(r.ExtraTags, sanitizeExternalTags(out.Tags)...)
	return r, true
}

func sanitizeExternalTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	seen := map[string]struct{}{}
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.ReplaceAll(tag, "|", ""))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
