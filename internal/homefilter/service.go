package homefilter

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/beck-8/subs-check/check"
)

type Service struct {
	mu        sync.Mutex
	cache     *cacheFile
	cachePath string
	logger    *log.Logger
}

func Run(addr, configPath string) error {
	if err := loadSubsCheckConfig(configPath); err != nil {
		return err
	}
	if err := initResolver(); err != nil {
		return err
	}
	outputDir, err := resolveOutputDir(configPath)
	if err != nil {
		return err
	}
	stateDir, err := resolveStateDir(outputDir)
	if err != nil {
		return err
	}
	cachePath := stateDir + "/" + cacheFileName
	cache, err := loadCache(cachePath)
	if err != nil {
		return err
	}

	svc := &Service{
		cache:     cache,
		cachePath: cachePath,
		logger:    log.Default(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/filter", svc.handleFilter)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	svc.logger.Printf("home filter service listening on http://%s/filter", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Service) handleFilter(w http.ResponseWriter, req *http.Request) {
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
