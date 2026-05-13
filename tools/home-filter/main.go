package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/beck-8/subs-check/check"
	"github.com/beck-8/subs-check/config"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/dns"
	"gopkg.in/yaml.v3"
)

const (
	maxConcurrent        = 5
	requestTimeout       = 5 * time.Second
	cacheTTL             = 7 * 24 * time.Hour
	maxLiveQueries       = 1000
	globalFailureRatio   = 0.30
	cacheFileName        = "cache.json"
	homeTag              = "HOME"
	ipapiEndpoint        = "https://api.ipapi.is/"
	defaultUserAgent     = "subs-check-home-filter/1.0"
	defaultBootstrapDNS1 = "223.5.5.5"
	defaultBootstrapDNS2 = "119.29.29.29"
)

type proxyDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
}

type cacheFile struct {
	ByIP          map[string]cachedDecision `json:"by_ip"`
	ByFingerprint map[string]fingerprintRef `json:"by_fingerprint"`
}

type cachedDecision struct {
	ExitIP      string    `json:"exit_ip"`
	Keep        bool      `json:"keep"`
	CheckedAt   time.Time `json:"checked_at"`
	CompanyType string    `json:"company_type,omitempty"`
}

type fingerprintRef struct {
	ExitIP    string    `json:"exit_ip"`
	CheckedAt time.Time `json:"checked_at"`
}

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
	Index        int
	Proxy        map[string]any
	Keep         bool
	ExitIP       string
	CompanyType  string
	FromCache    bool
	Err          error
	Fingerprint  string
	DecisionTime time.Time
}

type queryTask struct {
	Index       int
	Proxy       map[string]any
	Fingerprint string
}

var cfgPathFlag = flag.String("config", "", "subs-check config.yaml path")
var serveAddrFlag = flag.String("serve", "", "listen address for external-filter service, for example 127.0.0.1:8399")

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	configPath, err := discoverConfigPath()
	if err != nil {
		log.Fatalf("discover config path: %v", err)
	}
	if err := loadSubsCheckConfig(configPath); err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := initResolver(); err != nil {
		log.Fatalf("init resolver: %v", err)
	}

	outputDir, err := resolveOutputDir(configPath)
	if err != nil {
		log.Fatalf("resolve output dir: %v", err)
	}
	stateDir, err := resolveStateDir(outputDir)
	if err != nil {
		log.Fatalf("resolve state dir: %v", err)
	}
	cachePath := filepath.Join(stateDir, cacheFileName)
	cache, err := loadCache(cachePath)
	if err != nil {
		log.Fatalf("load cache: %v", err)
	}
	if strings.TrimSpace(*serveAddrFlag) != "" {
		if err := serveFilter(*serveAddrFlag, cachePath, cache); err != nil {
			log.Fatalf("serve external filter: %v", err)
		}
		return
	}

	allPath := filepath.Join(outputDir, "all.yaml")
	original, err := os.ReadFile(allPath)
	if err != nil {
		log.Fatalf("read %s: %v", allPath, err)
	}

	var doc proxyDoc
	if err := yaml.Unmarshal(original, &doc); err != nil {
		log.Fatalf("parse %s: %v", allPath, err)
	}
	log.Printf("loaded proxies=%d from %s", len(doc.Proxies), allPath)

	results, attempts, failures, err := classifyProxies(doc.Proxies, cache)
	if err != nil {
		log.Fatalf("classify proxies: %v", err)
	}
	if attempts > 0 && float64(failures)/float64(attempts) > globalFailureRatio {
		log.Fatalf("global API failure ratio too high: failures=%d attempts=%d ratio=%.2f; keeping existing outputs", failures, attempts, float64(failures)/float64(attempts))
	}
	if attempts > 0 && failures == attempts {
		log.Fatalf("all live ipapi.is probes failed (%d/%d); keeping existing outputs", failures, attempts)
	}

	filtered := proxyDoc{Proxies: make([]map[string]any, 0, len(results))}
	kept := 0
	for _, res := range results {
		if res.Err != nil || !res.Keep {
			continue
		}
		proxyCopy := cloneMap(res.Proxy)
		proxyCopy["name"] = appendHomeTag(proxyName(proxyCopy))
		filtered.Proxies = append(filtered.Proxies, proxyCopy)
		kept++
	}

	filteredYAML, err := yaml.Marshal(&filtered)
	if err != nil {
		log.Fatalf("marshal filtered yaml: %v", err)
	}

	var mihomoData, base64Data []byte
	if strings.TrimSpace(config.GlobalConfig.SubStorePort) != "" {
		baseURL := subStoreBaseURL()
		if err := patchSubStore(baseURL, filteredYAML); err != nil {
			log.Fatalf("refresh sub-store: %v; keeping existing outputs", err)
		}
		mihomoData, err = fetchURL(fmt.Sprintf("%s/api/file/mihomo", baseURL), 30*time.Second)
		if err != nil {
			log.Fatalf("fetch mihomo.yaml: %v; keeping existing outputs", err)
		}
		base64Data, err = fetchURL(fmt.Sprintf("%s/download/sub?target=V2Ray", baseURL), 30*time.Second)
		if err != nil {
			log.Fatalf("fetch base64.txt: %v; keeping existing outputs", err)
		}
	}

	if err := writeAtomically(allPath, filteredYAML, 0o644); err != nil {
		log.Fatalf("write %s: %v", allPath, err)
	}
	if len(mihomoData) > 0 {
		if err := writeAtomically(filepath.Join(outputDir, "mihomo.yaml"), mihomoData, 0o644); err != nil {
			log.Fatalf("write mihomo.yaml: %v", err)
		}
	}
	if len(base64Data) > 0 {
		if err := writeAtomically(filepath.Join(outputDir, "base64.txt"), base64Data, 0o644); err != nil {
			log.Fatalf("write base64.txt: %v", err)
		}
	}
	if err := saveCache(cachePath, cache); err != nil {
		log.Fatalf("save cache: %v", err)
	}

	cacheHits := 0
	liveHits := 0
	for _, res := range results {
		if res.FromCache {
			cacheHits++
		} else if res.Err == nil {
			liveHits++
		}
	}
	log.Printf("home filter done: input=%d kept=%d dropped=%d cache_hits=%d live_success=%d live_failures=%d output=%s", len(doc.Proxies), kept, len(doc.Proxies)-kept, cacheHits, liveHits, failures, allPath)
}

func discoverConfigPath() (string, error) {
	candidates := []string{}
	if *cfgPathFlag != "" {
		candidates = append(candidates, *cfgPathFlag)
	}
	if env := strings.TrimSpace(os.Getenv("SUBS_CHECK_HOME_FILTER_CONFIG")); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, filepath.Join("config", "config.yaml"))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && !st.IsDir() {
			return abs, nil
		}
	}
	return "", fmt.Errorf("no config.yaml found; checked %v", candidates)
}

func loadSubsCheckConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, config.GlobalConfig); err != nil {
		return err
	}
	if config.GlobalConfig.Timeout <= 0 {
		config.GlobalConfig.Timeout = 5000
	}
	return nil
}

func resolveOutputDir(configPath string) (string, error) {
	if strings.TrimSpace(config.GlobalConfig.OutputDir) != "" {
		return config.GlobalConfig.OutputDir, os.MkdirAll(config.GlobalConfig.OutputDir, 0o755)
	}
	configDir := filepath.Dir(configPath)
	repoRoot := filepath.Dir(configDir)
	out := filepath.Join(repoRoot, "output")
	return out, os.MkdirAll(out, 0o755)
}

func resolveStateDir(outputDir string) (string, error) {
	if env := strings.TrimSpace(os.Getenv("SUBS_CHECK_HOME_FILTER_STATE_DIR")); env != "" {
		return env, os.MkdirAll(env, 0o755)
	}
	dir := filepath.Join(outputDir, "home-filter-state")
	return dir, os.MkdirAll(dir, 0o755)
}

func loadCache(path string) (*cacheFile, error) {
	cache := &cacheFile{ByIP: map[string]cachedDecision{}, ByFingerprint: map[string]fingerprintRef{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cache, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cache, nil
	}
	if err := json.Unmarshal(data, cache); err != nil {
		return nil, err
	}
	if cache.ByIP == nil {
		cache.ByIP = map[string]cachedDecision{}
	}
	if cache.ByFingerprint == nil {
		cache.ByFingerprint = map[string]fingerprintRef{}
	}
	pruneCache(cache, time.Now())
	return cache, nil
}

func saveCache(path string, cache *cacheFile) error {
	pruneCache(cache, time.Now())
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomically(path, data, 0o644)
}

func pruneCache(cache *cacheFile, now time.Time) {
	for ip, item := range cache.ByIP {
		if now.Sub(item.CheckedAt) > cacheTTL {
			delete(cache.ByIP, ip)
		}
	}
	for fp, ref := range cache.ByFingerprint {
		if now.Sub(ref.CheckedAt) > cacheTTL {
			delete(cache.ByFingerprint, fp)
			continue
		}
		if _, ok := cache.ByIP[ref.ExitIP]; !ok {
			delete(cache.ByFingerprint, fp)
		}
	}
}

func classifyProxies(proxies []map[string]any, cache *cacheFile) ([]probeResult, int, int, error) {
	now := time.Now()
	results := make([]probeResult, len(proxies))
	tasks := make([]queryTask, 0, len(proxies))
	for i, proxy := range proxies {
		fp, err := proxyFingerprint(proxy)
		if err != nil {
			results[i] = probeResult{Index: i, Proxy: proxy, Err: err}
			continue
		}
		if decision, ok := cachedByFingerprint(cache, fp, now); ok {
			results[i] = probeResult{
				Index:        i,
				Proxy:        proxy,
				Keep:         decision.Keep,
				ExitIP:       decision.ExitIP,
				CompanyType:  decision.CompanyType,
				FromCache:    true,
				Fingerprint:  fp,
				DecisionTime: decision.CheckedAt,
			}
			continue
		}
		tasks = append(tasks, queryTask{Index: i, Proxy: proxy, Fingerprint: fp})
	}

	if len(tasks) > maxLiveQueries {
		return nil, 0, 0, fmt.Errorf("uncached live queries=%d exceeds limit=%d", len(tasks), maxLiveQueries)
	}
	if len(tasks) == 0 {
		return results, 0, 0, nil
	}

	jobs := make(chan queryTask)
	out := make(chan probeResult, len(tasks))
	var wg sync.WaitGroup
	workerCount := maxConcurrent
	if len(tasks) < workerCount {
		workerCount = len(tasks)
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				out <- runLiveProbe(task)
			}
		}()
	}
	go func() {
		for _, task := range tasks {
			jobs <- task
		}
		close(jobs)
		wg.Wait()
		close(out)
	}()

	attempts := 0
	failures := 0
	for res := range out {
		results[res.Index] = res
		attempts++
		if res.Err != nil {
			failures++
			continue
		}
		cache.ByIP[res.ExitIP] = cachedDecision{
			ExitIP:      res.ExitIP,
			Keep:        res.Keep,
			CheckedAt:   res.DecisionTime,
			CompanyType: res.CompanyType,
		}
		cache.ByFingerprint[res.Fingerprint] = fingerprintRef{
			ExitIP:    res.ExitIP,
			CheckedAt: res.DecisionTime,
		}
	}

	return results, attempts, failures, nil
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

type filterService struct {
	mu        sync.Mutex
	cache     *cacheFile
	cachePath string
}

func serveFilter(addr, cachePath string, cache *cacheFile) error {
	svc := &filterService{cache: cache, cachePath: cachePath}
	mux := http.NewServeMux()
	mux.HandleFunc("/filter", svc.handleFilter)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Printf("home filter service listening on http://%s/filter", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *filterService) classify(proxy map[string]any) probeResult {
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

	res := runLiveProbe(queryTask{Proxy: proxy, Fingerprint: fp})
	if res.Err != nil {
		return res
	}

	s.mu.Lock()
	s.cache.ByIP[res.ExitIP] = cachedDecision{ExitIP: res.ExitIP, Keep: res.Keep, CheckedAt: res.DecisionTime, CompanyType: res.CompanyType}
	s.cache.ByFingerprint[res.Fingerprint] = fingerprintRef{ExitIP: res.ExitIP, CheckedAt: res.DecisionTime}
	if err := saveCache(s.cachePath, s.cache); err != nil {
		log.Printf("save cache failed: %v", err)
	}
	s.mu.Unlock()
	return res
}

func (s *filterService) handleFilter(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer req.Body.Close()
	var in check.ExternalFilterRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<20)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(in.Proxy) == 0 {
		http.Error(w, "missing proxy", http.StatusBadRequest)
		return
	}

	res := s.classify(in.Proxy)

	out := check.ExternalFilterResponse{Keep: res.Err == nil && res.Keep}
	if res.Err != nil {
		out.Reason = res.Err.Error()
	} else if res.Keep {
		out.Tags = []string{homeTag}
	} else {
		out.Reason = "not residential isp"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func runLiveProbe(task queryTask) probeResult {
	res := probeResult{Index: task.Index, Proxy: task.Proxy, Fingerprint: task.Fingerprint}
	client := check.CreateClient(task.Proxy)
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

func appendHomeTag(name string) string {
	parts := strings.Split(name, "|")
	for _, part := range parts {
		if strings.TrimSpace(part) == homeTag {
			return name
		}
	}
	if name == "" {
		return homeTag
	}
	return name + "|" + homeTag
}

func proxyName(proxy map[string]any) string {
	if name, ok := proxy["name"].(string); ok {
		return name
	}
	return ""
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

func cloneMap(src map[string]any) map[string]any {
	data, _ := json.Marshal(src)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func subStoreBaseURL() string {
	port := strings.TrimSpace(config.GlobalConfig.SubStorePort)
	if strings.Contains(port, ":") {
		parts := strings.Split(port, ":")
		port = ":" + parts[len(parts)-1]
	} else if port != "" {
		port = ":" + port
	}
	baseURL := "http://127.0.0.1" + port
	if strings.TrimSpace(config.GlobalConfig.SubStorePath) != "" {
		baseURL += config.GlobalConfig.SubStorePath
	}
	return baseURL
}

func patchSubStore(baseURL string, yamlData []byte) error {
	payload := map[string]any{
		"content": string(yamlData),
		"name":    "sub",
		"remark":  "subs-check专用,勿动",
		"source":  "local",
		"process": []map[string]any{{"type": "Quick Setting Operator"}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/sub/%s", baseURL, "sub"), bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("sub-store PATCH /api/sub/sub status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func fetchURL(rawURL string, timeout time.Duration) ([]byte, error) {
	resp, err := (&http.Client{Timeout: timeout}).Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s status=%d body=%s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func writeAtomically(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func initResolver() error {
	c := &config.GlobalConfig.DNS
	resolver.DisableIPv6 = !c.IPv6
	if !c.Enable {
		return nil
	}
	if len(c.DefaultNameserver) == 0 {
		c.DefaultNameserver = []string{defaultBootstrapDNS1, defaultBootstrapDNS2}
	}
	valid, err := validateBootstrapIPs(c.DefaultNameserver)
	if err != nil {
		return err
	}
	c.DefaultNameserver = valid
	if len(c.Nameserver) == 0 {
		c.Nameserver = c.DefaultNameserver
	}
	if len(c.ProxyServerNameserver) == 0 {
		c.ProxyServerNameserver = c.Nameserver
	}
	main, err := parseNameservers(c.Nameserver)
	if err != nil {
		return err
	}
	proxySrv, err := parseNameservers(c.ProxyServerNameserver)
	if err != nil {
		return err
	}
	def, err := parseNameservers(c.DefaultNameserver)
	if err != nil {
		return err
	}
	rs := dns.NewResolver(dns.Config{Main: main, Default: def, ProxyServer: proxySrv, IPv6: c.IPv6})
	resolver.DefaultResolver = rs.Resolver
	resolver.ProxyServerHostResolver = rs.ProxyResolver
	return nil
}

func parseNameservers(servers []string) ([]dns.NameServer, error) {
	out := make([]dns.NameServer, 0, len(servers))
	for _, s := range servers {
		raw := s
		if !strings.Contains(s, "://") {
			s = "udp://" + s
		}
		u, err := url.Parse(s)
		if err != nil {
			continue
		}
		ns := dns.NameServer{}
		switch u.Scheme {
		case "udp":
			ns.Addr = hostPort(u.Host, "53")
		case "tcp":
			ns.Net = "tcp"
			ns.Addr = hostPort(u.Host, "53")
		case "tls":
			ns.Net = "tls"
			ns.Addr = hostPort(u.Host, "853")
		case "https", "http":
			ns.Net = "https"
			defPort := "443"
			if u.Scheme == "http" {
				defPort = "80"
			}
			cleaned := url.URL{Scheme: u.Scheme, Host: hostPort(u.Host, defPort), Path: u.Path}
			ns.Addr = cleaned.String()
		case "quic":
			ns.Net = "quic"
			ns.Addr = hostPort(u.Host, "853")
		default:
			_ = raw
			continue
		}
		out = append(out, ns)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid nameserver")
	}
	return out, nil
}

func validateBootstrapIPs(servers []string) ([]string, error) {
	valid := make([]string, 0, len(servers))
	for _, ns := range servers {
		host := ns
		if h, _, err := net.SplitHostPort(ns); err == nil {
			host = h
		}
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		if net.ParseIP(host) == nil {
			continue
		}
		valid = append(valid, ns)
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("no valid bootstrap ip")
	}
	return valid, nil
}

func hostPort(host, defPort string) string {
	if host == "" {
		return ":" + defPort
	}
	if idx := strings.LastIndex(host, ":"); idx > strings.LastIndex(host, "]") {
		return host
	}
	return host + ":" + defPort
}
