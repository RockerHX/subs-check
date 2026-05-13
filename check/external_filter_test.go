package check

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beck-8/subs-check/config"
)

func TestExternalFilterClient_KeepAndAppendTags(t *testing.T) {
	var got ExternalFilterRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ExternalFilterResponse{Keep: true, Tags: []string{"HOME", "HOME", "bad|tag", " "}})
	}))
	defer server.Close()

	withConfig(t, config.Config{
		RenameNode:            false,
		Platforms:             []string{},
		ExternalFilterURL:     server.URL,
		ExternalFilterTimeout: 1,
	}, func() {
		client := NewExternalFilterClient()
		res, keep := client.Apply(context.Background(), Result{Proxy: map[string]any{"name": "node-a"}, IP: "1.1.1.1", Country: "US", IPRisk: "5%"})
		if !keep {
			t.Fatal("expected keep=true")
		}
		if got.Name != "node-a" || got.IP != "1.1.1.1" || got.Country != "US" || got.IPRisk != "5%" {
			t.Fatalf("unexpected request: %+v", got)
		}
		if name := RenderName(res, false); name != "node-a|HOME|badtag" {
			t.Fatalf("RenderName() = %q, want %q", name, "node-a|HOME|badtag")
		}
	})
}

func TestExternalFilterClient_Drop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ExternalFilterResponse{Keep: false, Reason: "not home"})
	}))
	defer server.Close()

	withConfig(t, config.Config{ExternalFilterURL: server.URL, ExternalFilterTimeout: 1}, func() {
		client := NewExternalFilterClient()
		_, keep := client.Apply(context.Background(), Result{Proxy: map[string]any{"name": "node-a"}})
		if keep {
			t.Fatal("expected keep=false")
		}
	})
}
