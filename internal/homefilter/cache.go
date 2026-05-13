package homefilter

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

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
