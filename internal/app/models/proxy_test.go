package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunModelsMistralThroughProxy(t *testing.T) {
	const token = "test-scoped-mistral-client-token"
	for _, tc := range []struct {
		name, selection, catalog, wantPath, wantError string
	}{
		{"granted", "", `{"providers":[{"id":"mistral","family":"inference","routes":[{"name":"mist","local_prefix":"/mistral-api/","profile":"mistral"}]}]}`, "/mistral-api/models", ""},
		{"not-granted", "", `{"providers":[]}`, "/_proxy/client/unavailable/mistral/models", "mistral is not granted to this client"},
		{"selected-but-not-granted", "  providers: [mistral]\n", `{"providers":[]}`, "/_proxy/client/unavailable/mistral/models", "mistral is not granted to this client"},
		{"opted-out", "  direct: [mistral]\n", `{"providers":[]}`, "", "requires base_url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("expected an authenticated GET using the scoped client token")
				}
				switch r.URL.Path {
				case "/_proxy/client/v2/providers":
					fmt.Fprint(w, tc.catalog)
				case tc.wantPath:
					requests.Add(1)
					if tc.wantError != "" {
						http.Error(w, tc.wantError, http.StatusForbidden)
						return
					}
					fmt.Fprint(w, `{"data":[{"id":"mistral-small-latest"}]}`)
				default:
					t.Errorf("unexpected proxy request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			dir := filepath.Join(home, "term-llm")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			data := fmt.Sprintf("proxy:\n  url: %s\n  token: %s\n%s", server.URL, token, tc.selection)
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}

			err := (&Options{Provider: "mistral"}).Run(nil, nil)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("models error = %v, want %q", err, tc.wantError)
			}
			var wantRequests int32
			if tc.wantPath != "" {
				wantRequests = 1
			}
			if got := requests.Load(); got != wantRequests {
				t.Fatalf("model catalog requests = %d, want %d", got, wantRequests)
			}
		})
	}
}
