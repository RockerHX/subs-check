package homefilter

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/beck-8/subs-check/config"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/dns"
	"gopkg.in/yaml.v3"
)

const (
	defaultBootstrapDNS1 = "223.5.5.5"
	defaultBootstrapDNS2 = "119.29.29.29"
)

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
