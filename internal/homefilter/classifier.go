package homefilter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/beck-8/subs-check/check"
)

const (
	cacheTTL         = 7 * 24 * time.Hour
	requestTimeout   = 5 * time.Second
	cacheFileName    = "cache.json"
	homeTag          = "HOME"
	ipapiEndpoint    = "https://api.ipapi.is/"
	defaultUserAgent = "subs-check-home-filter/1.0"
)

type ipapiResponse struct {
	IP           string `json:"ip"`
	IsProxy      bool   `json:"is_proxy"`
	IsVPN        bool   `json:"is_vpn"`
	IsTor        bool   `json:"is_tor"`
	IsDatacenter bool   `json:"is_datacenter"`
	IsMobile     bool   `json:"is_mobile"`
	Company      struct {
		Type string `json:"type"`
	} `json:"company"`
}

type probeResult struct {
	Proxy        map[string]any
	Keep         bool
	ExitIP       string
	CompanyType  string
	FromCache    bool
	Err          error
	Fingerprint  string
	DecisionTime time.Time
}

func (s *Service) classify(proxy map[string]any) probeResult {
	fp, err := proxyFingerprint(proxy)
	if err != nil {
		return probeResult{Proxy: proxy, Err: err}
	}

	s.mu.Lock()
	if decision, ok := cachedByFingerprint(s.cache, fp, time.Now()); ok {
		s.mu.Unlock()
		return probeResult{
			Proxy:        proxy,
			Keep:         decision.Keep,
			ExitIP:       decision.ExitIP,
			CompanyType:  decision.CompanyType,
			FromCache:    true,
			Fingerprint:  fp,
			DecisionTime: decision.CheckedAt,
		}
	}
	s.mu.Unlock()

	res := runLiveProbe(proxy, fp)
	if res.Err != nil {
		return res
	}

	s.mu.Lock()
	s.cache.ByIP[res.ExitIP] = cachedDecision{ExitIP: res.ExitIP, Keep: res.Keep, CheckedAt: res.DecisionTime, CompanyType: res.CompanyType}
	s.cache.ByFingerprint[res.Fingerprint] = fingerprintRef{ExitIP: res.ExitIP, CheckedAt: res.DecisionTime}
	if err := saveCache(s.cachePath, s.cache); err != nil {
		s.logger.Printf("save cache failed: %v", err)
	}
	s.mu.Unlock()
	return res
}

func runLiveProbe(proxy map[string]any, fingerprint string) probeResult {
	res := probeResult{Proxy: proxy, Fingerprint: fingerprint}
	client := check.CreateClient(proxy)
	if client == nil {
		res.Err = errors.New("create proxy client failed")
		return res
	}
	defer client.Close()
	client.Timeout = requestTimeout

	req, err := http.NewRequest(http.MethodGet, ipapiEndpoint, nil)
	if err != nil {
		res.Err = err
		return res
	}
	req.Header.Set("User-Agent", defaultUserAgent)

	httpRes, err := client.Do(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer httpRes.Body.Close()
	if httpRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(httpRes.Body, 1024))
		res.Err = fmt.Errorf("ipapi status=%d body=%s", httpRes.StatusCode, strings.TrimSpace(string(body)))
		return res
	}

	var payload ipapiResponse
	if err := json.NewDecoder(httpRes.Body).Decode(&payload); err != nil {
		res.Err = err
		return res
	}
	if strings.TrimSpace(payload.IP) == "" {
		res.Err = errors.New("ipapi response missing ip")
		return res
	}

	res.ExitIP = payload.IP
	res.CompanyType = strings.TrimSpace(payload.Company.Type)
	res.Keep = payload.Company.Type == "isp" && !payload.IsDatacenter && !payload.IsProxy && !payload.IsVPN && !payload.IsTor && !payload.IsMobile
	res.DecisionTime = time.Now()
	return res
}

func cachedByFingerprint(cache *cacheFile, fp string, now time.Time) (cachedDecision, bool) {
	ref, ok := cache.ByFingerprint[fp]
	if !ok || now.Sub(ref.CheckedAt) > cacheTTL {
		return cachedDecision{}, false
	}
	decision, ok := cache.ByIP[ref.ExitIP]
	if !ok || now.Sub(decision.CheckedAt) > cacheTTL {
		return cachedDecision{}, false
	}
	return decision, true
}

func proxyFingerprint(proxy map[string]any) (string, error) {
	normalized := normalizeValue(stripEphemeralFields(proxy))
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func stripEphemeralFields(proxy map[string]any) map[string]any {
	out := make(map[string]any, len(proxy))
	for k, v := range proxy {
		switch k {
		case "name", "sub_tag", "sub_url":
			continue
		default:
			out[k] = v
		}
	}
	return out
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = normalizeValue(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalizeValue(t[i])
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalizeValue(t[i])
		}
		return out
	default:
		return v
	}
}
