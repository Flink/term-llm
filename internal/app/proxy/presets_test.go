package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRoutePresetsCompileAndForwardNativeCredentials(t *testing.T) {
	seen := map[string]bool{}
	for _, preset := range routePresets {
		t.Run(preset.ID, func(t *testing.T) {
			if seen[preset.ID] || !slug.MatchString(preset.ID) || preset.Label == "" {
				t.Fatal("invalid or duplicate preset", preset.ID)
			}
			seen[preset.ID] = true
			if preset.Upstream == "" {
				if preset.ID != "vllm" {
					t.Fatal("unexpected missing URL")
				}
				return
			}
			target, err := url.Parse(preset.Upstream)
			if err != nil {
				t.Fatal(err)
			}
			if target.Scheme != "https" && preset.ID != "ollama" {
				t.Fatal("unexpected insecure preset")
			}
			auth := preset.Auth
			if auth.Header == "" {
				auth = bearerPresetAuth
			}
			headers := map[string]HeaderRule{}
			materials := map[string]material{}
			if preset.Auth.Header != "" {
				headers[preset.Auth.Header] = HeaderRule{Secret: "key", Prefix: preset.Auth.Prefix}
				materials["key"] = boundMaterial(t, preset.Upstream, "upstream-test-key")
			}
			r := Route{Name: preset.ID, LocalPrefix: "/" + preset.ID + "/", Upstream: preset.Upstream, Enabled: true, AllowHTTP: preset.ID == "ollama", CallerAuth: auth, SetHeaders: headers}
			if _, err := compile(Config{Routes: []Route{r}}, 1, materials); err != nil {
				t.Fatal(err)
			}
			suffix := "chat/completions"
			switch preset.ID {
			case "anthropic", "opencode-go-messages":
				suffix = "v1/messages"
			case "gemini":
				suffix = "models/test:streamGenerateContent"
			case "jina", "voyage":
				suffix = "embeddings"
			case "elevenlabs":
				suffix = "v1/text-to-speech/test"
			case "mistral":
				suffix = "audio/transcriptions"
			case "ollama":
				suffix = "api/chat"
			}
			var requests atomic.Int32
			token := ""
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				if req.URL.Path != strings.TrimSuffix(target.Path, "/")+"/"+suffix || req.URL.RawQuery != "alt=sse" {
					t.Errorf("wrong upstream URL: %s", req.URL)
				}
				for _, values := range req.Header {
					for _, value := range values {
						if strings.Contains(value, token) {
							t.Error("caller token forwarded upstream")
						}
					}
				}
				if preset.Auth.Header != "" && req.Header.Get(preset.Auth.Header) != preset.Auth.Prefix+"upstream-test-key" {
					t.Error("wrong upstream auth")
				}
				if preset.Auth.Header != "Authorization" && req.Header.Get("Authorization") != "" {
					t.Error("unexpected Authorization")
				}
				body, _ := io.ReadAll(req.Body)
				if string(body) != `{"model":"test"}` {
					t.Error("request body changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {}\n\n")
			}))
			defer up.Close()
			s := testService(t)
			r.Upstream = up.URL + target.Path
			r.AllowHTTP = true
			if preset.Auth.Header != "" {
				materials["key"] = boundMaterial(t, r.Upstream, "upstream-test-key")
			}
			configure(t, s, Config{Routes: []Route{r}}, materials)
			if requests.Load() != 0 {
				t.Fatal("configuration probed upstream")
			}
			token = caller(t, s, r.Name)
			req := httptest.NewRequest(http.MethodPost, r.LocalPrefix+suffix+"?alt=sse", strings.NewReader(`{"model":"test"}`))
			req.Header.Set(auth.Header, auth.Prefix+token)
			result := httptest.NewRecorder()
			s.ServeHTTP(result, req)
			if result.Code != 200 || result.Body.String() != "data: {}\n\n" || requests.Load() != 1 {
				t.Fatalf("forwarding failed: %d %s", result.Code, result.Body.String())
			}
		})
	}
	if len(seen) != 18 {
		t.Fatalf("expected 18 preset/template entries, got %d", len(seen))
	}
}

func TestRoutePresetsRequireAuthenticatedState(t *testing.T) {
	s := testService(t)
	if result := adminRequest(s, "GET", "state", nil, nil, ""); result.Code != http.StatusUnauthorized {
		t.Fatal(result.Code)
	}
	cookie, csrf := loginAdmin(t, s)
	result := adminRequest(s, "GET", "state", nil, cookie, csrf)
	var state struct {
		Presets []routePreset `json:"presets"`
	}
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &state) != nil || len(state.Presets) != len(routePresets) {
		t.Fatalf("missing presets: %d %s", result.Code, result.Body.String())
	}
}
