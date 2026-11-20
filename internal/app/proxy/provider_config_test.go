package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/providerspec"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

const testDeepSeekProviderYAML = `type: vllm
model: deepseek-ai/DeepSeek-V4-Flash
context_window: 200000
max_output_tokens: 50000
vllm_thinking_param: thinking
vision_via: cdck_qwen
models:
  - id: deepseek-ai/DeepSeek-V4-Flash
    alias: deepseek-v4-flash
    reasoning_efforts: [none, low, high, max]
    default_reasoning_effort: high
`

func TestClientProvidersDiscoveryAndGrantPreservation(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)

	// Create two routes atomically with provider_config_yaml for cdck_deepseek.
	routes := []Route{
		{Name: "cdck_deepseek", LocalPrefix: "/cdck_deepseek/", Upstream: "https://snorlax.example.test/v1", Enabled: true, CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}},
		{Name: "cdck_qwen", LocalPrefix: "/cdck_qwen/", Upstream: "https://qwen.example.test/v1", Enabled: true, CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}},
		{Name: "disabled_vllm", LocalPrefix: "/disabled_vllm/", Upstream: "https://disabled.example.test/v1", Enabled: false, CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}},
	}
	res := proxyResponse[map[string]int64](t, adminRequest(s, "PUT", "config", map[string]any{
		"revision": s.snap.revision,
		"routes":   routes,
		"provider_config_yaml": map[string]string{
			"cdck_deepseek": testDeepSeekProviderYAML,
			"disabled_vllm": "type: vllm\nmodel: hidden\n",
		},
	}, cookie, csrf), http.StatusOK)

	// Grant client only cdck_deepseek and disabled_vllm (NOT cdck_qwen).
	issued := proxyResponse[issuedBrowserCaller](t, adminRequest(s, "POST", "callers", map[string]any{
		"name":      "worker",
		"providers": []string{"cdck_deepseek", "disabled_vllm"},
		"revision":  res["revision"],
	}, cookie, csrf), http.StatusCreated)

	// Unauthenticated and admin-cookie requests must be rejected.
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/_proxy/client/v2/providers", nil),
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/_proxy/client/v2/providers", nil)
			r.AddCookie(cookie)
			return r
		}(),
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/_proxy/client/v2/providers", nil)
			r.Header.Set("Authorization", "Bearer bad-token")
			return r
		}(),
	} {
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%s)", rr.Code, rr.Body.String())
		}
	}

	// Admin API key must also be rejected on client discovery.
	adminKey := issueAdminKey(t, s, cookie, csrf, "provisioner")
	req := httptest.NewRequest(http.MethodGet, "/_proxy/client/v2/providers", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey.Token)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("admin key accepted on /_proxy/client/v2/providers: %d", rr.Code)
	}

	// Valid client token returns ONLY granted & enabled cdck_deepseek.
	fetch := func() struct {
		Revision  int64                `json:"revision"`
		Providers []clientProviderInfo `json:"providers"`
	} {
		r := httptest.NewRequest(http.MethodGet, "/_proxy/client/v2/providers", nil)
		r.Header.Set("Authorization", "Bearer "+issued.Token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("providers status = %d: %s", w.Code, w.Body.String())
		}
		var out struct {
			Revision  int64                `json:"revision"`
			Providers []clientProviderInfo `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := fetch()
	if len(got.Providers) != 1 || got.Providers[0].ID != "cdck_deepseek" {
		t.Fatalf("expected only cdck_deepseek, got %+v", got.Providers)
	}
	cfg := got.Providers[0].Config
	if cfg == nil || cfg.Type != providerspec.TypeVLLM || cfg.ContextWindow != 200000 || cfg.VLLMThinkingParam != "thinking" || cfg.VisionVia != "cdck_qwen" {
		t.Fatalf("unexpected discovered config: %+v", cfg)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Alias != "deepseek-v4-flash" || cfg.Models[0].DefaultReasoningEffort != "high" {
		t.Fatalf("unexpected discovered models: %+v", cfg.Models)
	}

	// Updating provider config via PUT /_proxy/api/provider-config must NOT drop grants.
	updated := proxyResponse[map[string]any](t, adminRequest(s, "PUT", "provider-config", map[string]any{
		"revision": s.snap.revision,
		"provider": "cdck_deepseek",
		"yaml":     "type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash\ncontext_window: 262144\n",
	}, cookie, csrf), http.StatusOK)
	if int64(updated["revision"].(float64)) <= got.Revision {
		t.Fatalf("revision did not advance: %v vs %d", updated["revision"], got.Revision)
	}
	got2 := fetch()
	if len(got2.Providers) != 1 || got2.Providers[0].Config == nil || got2.Providers[0].Config.ContextWindow != 262144 {
		t.Fatalf("grant was dropped or config not updated: %+v", got2.Providers)
	}

	// Clearing provider config removes config while keeping the route discoverable.
	proxyResponse[map[string]any](t, adminRequest(s, "PUT", "provider-config", map[string]any{
		"revision": s.snap.revision,
		"provider": "cdck_deepseek",
		"yaml":     "",
	}, cookie, csrf), http.StatusOK)
	got3 := fetch()
	if len(got3.Providers) != 1 || got3.Providers[0].Config != nil {
		t.Fatalf("expected route without config after clear, got %+v", got3.Providers)
	}
}

func TestProviderConfigParseExportImportAndFileMode(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)

	// Parse pasted term-llm provider YAML.
	pasted := `cdck_deepseek:
  enabled: false
  type: vllm
  base_url: https://snorlax1b.dub2.discourse.cloud:34567/v1
  api_key: $(op-cache read "op://Everybody/Snorlax LLM/credential")
  model: deepseek-ai/DeepSeek-V4-Flash
  vllm_thinking_param: thinking
`
	parsed := proxyResponse[proxyprofile.ParsedTermLLMProvider](t, adminRequest(s, "POST", "provider-config/parse", map[string]any{
		"yaml": pasted,
	}, cookie, csrf), http.StatusOK)
	if parsed.Name != "cdck_deepseek" || parsed.Upstream != "https://snorlax1b.dub2.discourse.cloud:34567/v1" || parsed.Config.Type != providerspec.TypeVLLM {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}

	// Reject forbidden field on direct save.
	routes := []Route{{Name: "cdck_deepseek", LocalPrefix: "/cdck_deepseek/", Upstream: "https://snorlax1b.dub2.discourse.cloud:34567/v1", Enabled: true, CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}}}
	proxyResponse[map[string]any](t, adminRequest(s, "PUT", "config", map[string]any{
		"revision": s.snap.revision,
		"routes":   routes,
	}, cookie, csrf), http.StatusOK)
	bad := adminRequest(s, "PUT", "provider-config", map[string]any{
		"revision": s.snap.revision,
		"provider": "cdck_deepseek",
		"yaml":     "type: vllm\napi_key: secret\n",
	}, cookie, csrf)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "api_key") {
		t.Fatalf("expected 400 rejecting api_key, got %d: %s", bad.Code, bad.Body.String())
	}

	// Save valid config, then verify export/import round-trip and state attachment.
	proxyResponse[map[string]any](t, adminRequest(s, "PUT", "provider-config", map[string]any{
		"revision": s.snap.revision,
		"provider": "cdck_deepseek",
		"yaml":     parsed.ConfigYAML,
	}, cookie, csrf), http.StatusOK)

	state := proxyResponse[struct {
		Providers []adminProviderInfo `json:"providers"`
	}](t, adminRequest(s, "GET", "state", nil, cookie, csrf), http.StatusOK)
	if len(state.Providers) != 1 || state.Providers[0].Config == nil || state.Providers[0].Config.VLLMThinkingParam != "thinking" || !strings.Contains(state.Providers[0].ConfigYAML, "vllm_thinking_param: thinking") {
		t.Fatalf("state missing provider config: %+v", state.Providers)
	}

	exportRec := adminRequest(s, "GET", "export", nil, cookie, csrf)
	if exportRec.Code != http.StatusOK || !strings.Contains(exportRec.Body.String(), "providers:") || !strings.Contains(exportRec.Body.String(), "cdck_deepseek:") {
		t.Fatalf("export missing providers section: %d\n%s", exportRec.Code, exportRec.Body.String())
	}

	// File mode loads providers: section directly from proxy.yaml.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "proxy.yaml")
	if err := os.WriteFile(cfgPath, exportRec.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	adminFile := filepath.Join(dir, "admin-token")
	if err := os.WriteFile(adminFile, []byte(strings.Repeat("a", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Open(Options{
		StateDir:       filepath.Join(dir, "state"),
		AdminTokenFile: adminFile,
		ConfigFile:     cfgPath,
		Auth:           "token",
	})
	if err != nil {
		t.Fatalf("file mode open failed: %v", err)
	}
	defer svc.Close()
	if got := svc.snap.config.Providers["cdck_deepseek"]; got.Client == nil || got.Client.Type != providerspec.TypeVLLM || got.Client.VLLMThinkingParam != "thinking" {
		t.Fatalf("file mode did not load provider config: %+v", got)
	}
}

func TestEndToEndClientDiscoversProviderFromLiveProxyService(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)

	routes := []Route{
		{Name: "cdck_deepseek", LocalPrefix: "/cdck_deepseek/", Upstream: "https://snorlax1b.dub2.discourse.cloud:34567/v1", Enabled: true, CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}},
	}
	res := proxyResponse[map[string]int64](t, adminRequest(s, "PUT", "config", map[string]any{
		"revision": s.snap.revision,
		"routes":   routes,
		"provider_config_yaml": map[string]string{
			"cdck_deepseek": testDeepSeekProviderYAML,
		},
	}, cookie, csrf), http.StatusOK)
	issued := proxyResponse[issuedBrowserCaller](t, adminRequest(s, "POST", "callers", map[string]any{
		"name":      "e2e-client",
		"providers": []string{"cdck_deepseek"},
		"revision":  res["revision"],
	}, cookie, csrf), http.StatusCreated)

	srv := httptest.NewServer(s)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	cfgDir := filepath.Join(home, "term-llm")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	clientYAML := "default_provider: cdck_deepseek\nproxy:\n  url: " + srv.URL + "\n  token: " + issued.Token + "\n  providers: [cdck_deepseek]\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(clientYAML), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() against live proxy failed: %v", err)
	}
	pc := cfg.Providers["cdck_deepseek"]
	if pc.Type != config.ProviderTypeVLLM || pc.Model != "deepseek-ai/DeepSeek-V4-Flash" || pc.ContextWindow != 200000 || pc.MaxOutputTokens != 50000 || pc.VLLMThinkingParam != "thinking" || pc.VisionVia != "cdck_qwen" {
		t.Fatalf("unexpected e2e materialized provider: %+v", pc)
	}
	if pc.BaseURL != srv.URL+"/cdck_deepseek" || pc.ProxyBinding != srv.URL+"/cdck_deepseek" {
		t.Fatalf("unexpected e2e base URL: %+v", pc)
	}
	if got := config.DisplayModelForProviderModel(cfg, "cdck_deepseek", "deepseek-ai/DeepSeek-V4-Flash-high"); got != "deepseek-v4-flash-high" {
		t.Fatalf("DisplayModelForProviderModel = %q", got)
	}
}
