package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
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
		if r.URL.Path != "/_proxy/client/v1/providers" {
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
					"id":           "cdck_deepseek",
					"local_prefix": "/cdck_deepseek/",
					"websocket":    false,
					"routes":       []map[string]any{{"name": "cdck_deepseek", "local_prefix": "/cdck_deepseek/", "websocket": false}},
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
					"id":           "cdck_deepseek",
					"local_prefix": "/team/deepseek/",
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

	// Server returning unknown/forbidden field (e.g. api_key) in config is rejected fail-closed.
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"revision":1,"providers":[{"id":"cdck_deepseek","local_prefix":"/cdck_deepseek/","config":{"type":"vllm","api_key":"$(touch pwned)"}}]}`))
	}))
	defer evil.Close()
	writeTestConfig(t, home, `proxy:
  url: `+evil.URL+`
  token: scoped-evil-token-0123456789
  providers: [cdck_deepseek]
`)
	if _, err := LoadWithoutCredentials(); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected strict rejection of api_key in discovery JSON, got %v", err)
	}
}
