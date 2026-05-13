package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/beck-8/subs-check/internal/homefilter"
)

func main() {
	configPath := flag.String("config", "", "subs-check config.yaml path")
	serveAddr := flag.String("serve", defaultServeAddr(), "listen address for external-filter service")
	flag.Parse()

	resolvedConfig, err := discoverConfigPath(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := homefilter.Run(*serveAddr, resolvedConfig); err != nil {
		log.Fatal(err)
	}
}

func defaultServeAddr() string {
	if v := strings.TrimSpace(os.Getenv("SUBS_CHECK_HOME_FILTER_LISTEN")); v != "" {
		return v
	}
	return "127.0.0.1:8399"
}

func discoverConfigPath(flagValue string) (string, error) {
	candidates := []string{}
	if strings.TrimSpace(flagValue) != "" {
		candidates = append(candidates, flagValue)
	}
	if env := strings.TrimSpace(os.Getenv("SUBS_CHECK_HOME_FILTER_CONFIG")); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, filepath.Join("config", "config.yaml"))
	for _, candidate := range candidates {
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
