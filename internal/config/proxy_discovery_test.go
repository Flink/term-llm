package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func stubProxyDiscovery(t *testing.T, fn func(root, token string) (*proxyDiscoveryCatalog, error)) {
	t.Helper()
	resetProxyDiscoveryCache()
	orig := fetchProxyProviders
	fetchProxyProviders = fn
	t.Cleanup(func() {
		fetchProxyProviders = orig
		resetProxyDiscoveryCache()
	})
}

func stubEmptyProxyDiscovery(t *testing.T) {
	t.Helper()
	stubProxyDiscovery(t, func(_, _ string) (*proxyDiscoveryCatalog, error) {
		return &proxyDiscoveryCatalog{Providers: map[string]proxyDiscoveredProvider{}}, nil
	})
}

func TestProxyDiscoveryMaterializesCustomProviderOncePerProcess(t *testing.T) {
	resetProxyDiscoveryCache()
	t.Cleanup(resetProxyDiscoveryCache)

	const wantToken = "scoped-discovery-client-token-0123456789"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_proxy/client/v2/providers" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"revision": 7,
			"providers": []map[string]any{
				{
					"id":     "cdck_deepseek",
					"family": "inference",
					"routes": []map[string]any{{"name": "cdck_deepseek", "local_prefix": "/cdck_deepseek/", "websocket": false}},
					"config": map[string]any{
						"type":                "vllm",
						"model":               "deepseek-ai/DeepSeek-V4-Flash",
						"context_window":      200000,
						"max_output_tokens":   50000,
						"vllm_thinking_param": "thinking",
						"vision_via":          "cdck_qwen",
						"models": []any{
							map[string]any{
								"id":                       "deepseek-ai/DeepSeek-V4-Flash",
								"alias":                    "deepseek-v4-flash",
								"reasoning_efforts":        []string{"none", "low", "high", "max"},
								"default_reasoning_effort": "high",
							},
							"deepseek-ai/DeepSeek-V4-Lite",
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeTestConfig(t, home, `proxy:
  url: `+srv.URL+`
  token: `+wantToken+`
  providers: [cdck_deepseek]
`)

	cfg, err := LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pc, ok := cfg.Providers["cdck_deepseek"]
	if !ok {
		t.Fatal("cdck_deepseek was not materialized from proxy discovery")
	}
	if pc.Type != ProviderTypeVLLM {
		t.Fatalf("Type = %q, want vllm", pc.Type)
	}
	if pc.BaseURL != srv.URL+"/cdck_deepseek" || pc.ProxyBinding != srv.URL+"/cdck_deepseek" || pc.APIKey != wantToken {
		t.Fatalf("unexpected proxy binding: %+v", pc)
	}
	if pc.Model != "deepseek-ai/DeepSeek-V4-Flash" || pc.ContextWindow != 200000 || pc.MaxOutputTokens != 50000 || pc.VLLMThinkingParam != "thinking" || pc.VisionVia != "cdck_qwen" {
		t.Fatalf("unexpected discovered metadata: %+v", pc)
	}
	if len(pc.Models) != 2 || pc.Models[0] != "deepseek-v4-flash" || pc.Models[1] != "deepseek-ai/DeepSeek-V4-Lite" {
		t.Fatalf("unexpected Models: %#v", pc.Models)
	}
	if len(pc.ModelConfigs) != 1 || pc.ModelConfigs[0].ID != "deepseek-ai/DeepSeek-V4-Flash" || pc.ModelConfigs[0].DefaultReasoningEffort != "high" {
		t.Fatalf("unexpected ModelConfigs: %+v", pc.ModelConfigs)
	}
	if got := UpstreamModelForProviderModel(cfg, "cdck_deepseek", "deepseek-v4-flash-high"); got != "deepseek-ai/DeepSeek-V4-Flash-high" {
		t.Fatalf("UpstreamModelForProviderModel = %q", got)
	}
	if b, ok := proxyclient.Lookup(pc.BaseURL + "/chat/completions"); !ok || b.Token != wantToken {
		t.Fatalf("missing proxyclient binding: %+v, %v", b, ok)
	}

	// Second load in the same process must reuse the once-per-process discovery cache.
	if _, err := LoadWithoutCredentials(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("discovery HTTP calls = %d, want 1 (once per process)", got)
	}
}

func TestProxyDiscoveryLocalOverridesAndTypeConflict(t *testing.T) {
	resetProxyDiscoveryCache()
	t.Cleanup(resetProxyDiscoveryCache)

	const wantToken = "scoped-override-client-token-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"revision": 3,
			"providers": []map[string]any{
				{
					"id":     "cdck_deepseek",
					"family": "inference",
					"routes": []map[string]any{{"name": "backend", "local_prefix": "/team/deepseek/", "profile": "vllm"}},
					"config": map[string]any{
						"type":                "vllm",
						"model":               "deepseek-ai/DeepSeek-V4-Flash",
						"context_window":      200000,
						"vllm_thinking_param": "thinking",
						"vision_via":          "cdck_qwen",
					},
				},
			},
		})
	}))
	defer srv.Close()

	// Explicit local model & vision_via override proxy values, while keeping proxy type/context_window/local_prefix.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeTestConfig(t, home, `proxy:
  url: `+srv.URL+`
  token: `+wantToken+`
  providers: [cdck_deepseek]
providers:
  cdck_deepseek:
    model: local-preferred-model
    vision_via: gemini:gemini-2.5-pro
`)
	cfg, err := LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.Providers["cdck_deepseek"]
	if pc.Type != ProviderTypeVLLM || pc.ContextWindow != 200000 || pc.VLLMThinkingParam != "thinking" {
		t.Fatalf("missing proxy base fields: %+v", pc)
	}
	if pc.Model != "local-preferred-model" || pc.VisionVia != "gemini:gemini-2.5-pro" {
		t.Fatalf("local overrides did not win: %+v", pc)
	}
	if pc.BaseURL != srv.URL+"/team/deepseek" {
		t.Fatalf("BaseURL = %q, want custom local_prefix %q", pc.BaseURL, srv.URL+"/team/deepseek")
	}

	// Conflicting explicit local type fails closed.
	writeTestConfig(t, home, `proxy:
  url: `+srv.URL+`
  token: `+wantToken+`
  providers: [cdck_deepseek]
providers:
  cdck_deepseek:
    type: openai
`)
	if _, err := LoadWithoutCredentials(); err == nil || !strings.Contains(err.Error(), "conflicts with proxy-discovered type") {
		t.Fatalf("expected type conflict error, got %v", err)
	}
}

func TestProxyDiscoveryFailsHardOnErrorsAndRejectsUnsafeResponses(t *testing.T) {
	resetProxyDiscoveryCache()
	t.Cleanup(resetProxyDiscoveryCache)

	// Unreachable proxy fails hard even for built-in provider selection.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	writeTestConfig(t, home, `proxy:
  url: `+deadURL+`
  token: scoped-fail-hard-token-0123456789
  providers: [chatgpt]
`)
	if _, err := LoadWithoutCredentials(); err == nil || !strings.Contains(err.Error(), "proxy provider discovery failed") {
		t.Fatalf("expected fail-hard discovery error, got %v", err)
	}

	// Unauthorized proxy token fails hard.
	unauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer unauth.Close()
	writeTestConfig(t, home, `proxy:
  url: `+unauth.URL+`
  token: scoped-unauth-token-0123456789
  providers: [cdck_deepseek]
`)
	if _, err := LoadWithoutCredentials(); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized discovery error, got %v", err)
	}

	// Unknown fields are ignored and never materialized as credentials.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"revision":1,"future":true,"providers":[{"id":"cdck_deepseek","family":"inference","future":{},"routes":[{"name":"cdck_deepseek","local_prefix":"/cdck_deepseek/","profile":"vllm","future":[]},{"name":"new-backend","local_prefix":"/new/","profile":"future-profile"}],"config":{"type":"vllm","api_key":"$(must-not-run)"}}]}`))
	}))
	defer srv.Close()
	const token = "scoped-unknown-field-token-0123456789"
	cat, err := fetchProxyProvidersHTTP(srv.URL, token)
	if err != nil || len(cat.Providers["cdck_deepseek"].Routes) != 1 {
		t.Fatalf("unknown fields or profile did not get ignored: catalog=%+v err=%v", cat, err)
	}
	writeTestConfig(t, home, `proxy:
  url: `+srv.URL+`
  token: `+token+`
  providers: [cdck_deepseek]
`)
	cfg, err := LoadWithoutCredentials()
	if err != nil || cfg.Providers["cdck_deepseek"].APIKey != token {
		t.Fatalf("discovery did not use only the delegated credential: err=%v", err)
	}
}

func TestProxyDiscoveryV2ValidatesRoutesAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*proxyDiscoveryPayload)
		want   string
	}{
		{"valid", func(*proxyDiscoveryPayload) {}, ""},
		{"provider-slug", func(p *proxyDiscoveryPayload) { p.Providers[0].ID = "exa?" }, "provider id"},
		{"duplicate-provider", func(p *proxyDiscoveryPayload) { p.Providers[1].ID = "exa" }, "duplicate provider id"},
		{"slug", func(p *proxyDiscoveryPayload) { p.Providers[0].Routes[0].Name = "bad/name" }, "route name"},
		{"empty", func(p *proxyDiscoveryPayload) { p.Providers[0].Routes = nil }, "has no routes"},
		{"family", func(p *proxyDiscoveryPayload) { p.Providers[0].Family = "fetch" }, "unknown family"},
		{"config-family", func(p *proxyDiscoveryPayload) { p.Providers[0].Config = &proxyprofile.ClientProviderConfig{} }, "outside the inference family"},
		{"unsafe-prefix", func(p *proxyDiscoveryPayload) { p.Providers[0].Routes[0].LocalPrefix = "/v1/../foo/" }, "unsafe or duplicate local_prefix"},
		{"reserved-prefix", func(p *proxyDiscoveryPayload) {
			p.Providers[0].Routes[0].LocalPrefix = "/_proxy/client/unavailable/exa/"
		}, "unsafe or duplicate local_prefix"},
		{"duplicate-prefix", func(p *proxyDiscoveryPayload) { p.Providers[1].Routes[0].LocalPrefix = "/my-exa/" }, "unsafe or duplicate local_prefix"},
		{"duplicate-route", func(p *proxyDiscoveryPayload) { p.Providers[1].Routes[0].Name = "exa-rest" }, "duplicate route name"},
		{"cross-provider-collision", func(p *proxyDiscoveryPayload) { p.Providers[1].Routes[0].Name = "exa" }, "collides with an unrelated provider"},
		{"wrong-slot", func(p *proxyDiscoveryPayload) { p.Defaults["fetch"] = p.Defaults["search"] }, "cannot serve"},
		{"wrong-provider", func(p *proxyDiscoveryPayload) { p.Defaults["search"] = proxyDiscoveredDefault{"brave", "exa-rest"} }, "listed route"},
		{"missing-route", func(p *proxyDiscoveryPayload) { p.Defaults["search"] = proxyDiscoveredDefault{"exa", "exa-mcp"} }, "listed route"},
		{"future-profile", func(p *proxyDiscoveryPayload) { p.Providers[0].Routes[0].Profile = "future-exa" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := proxyDiscoveryPayload{Providers: []proxyDiscoveredProvider{
				familyProvider("exa", "search", familyRoute("exa-rest", "exa", "/my-exa/")),
				familyProvider("brave", "search", familyRoute("brave", "brave", "/brave/")),
			}, Defaults: map[string]proxyDiscoveredDefault{"search": {"exa", "exa-rest"}}}
			tc.change(&payload)
			cat, err := validateDiscoveryPayload(payload)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("wanted %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "future-profile" && (len(cat.Providers) != 1 || len(cat.Defaults) != 0) {
				t.Fatal("future route or its default was not skipped")
			}
		})
	}
}

func TestProxyDiscoveryV2ProbesV1OnHTTPError(t *testing.T) {
	const token = "test-old-proxy-client-token"
	for _, tc := range []struct {
		v2, v1 int
		want   string
	}{
		{401, 200, "proxy predates discovery v2; upgrade the proxy"},
		{403, 410, "check proxy.token"},
		{503, 404, "HTTP 503"},
	} {
		var probes atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("request did not use same scoped credentials")
			}
			switch r.URL.Path {
			case "/_proxy/client/v2/providers":
				w.WriteHeader(tc.v2)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			case "/_proxy/client/v1/providers":
				probes.Add(1)
				w.WriteHeader(tc.v1)
			default:
				t.Errorf("unexpected request %s", r.URL.Path)
			}
		}))
		cat, err := fetchProxyProvidersHTTP(srv.URL, token)
		srv.Close()
		if cat != nil || err == nil || !strings.Contains(err.Error(), tc.want) || probes.Load() != 1 {
			t.Fatalf("v2=%d v1=%d: err=%v, probes=%d", tc.v2, tc.v1, err, probes.Load())
		}
	}
}
