package responsesproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/cache"
	"github.com/term-llm/term-llm/internal/credentials"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestDelegatedCopilotCatalog(t *testing.T) {
	if !runProxyCatalogTestInChild(t) {
		return
	}
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const root = "https://copilot-catalog.example.test/route"
	caller := "sk-tlp-" + strings.Repeat("a", 64)
	binding := proxyclient.Binding{Root: root, Kind: "copilot", Token: caller + ".work"}
	if err := proxyclient.Register([]proxyclient.Binding{
		binding,
		{Root: root + "-other", Kind: "copilot", Token: binding.Token},
		{Root: root + "-wrong-kind", Kind: "grok", Token: binding.Token},
		{Root: root + "-shadowed", Kind: "copilot", Token: binding.Token},
		{Root: root + "-shadowed/models", Kind: "copilot", Token: "another-token"},
	}); err != nil {
		t.Fatal(err)
	}
	RefreshCopilotCacheSync([]ModelInfo{{ID: "direct"}})

	calls := 0
	status := http.StatusOK
	empty := false
	setCopilotCatalogTransport(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.String() != root+"/models" {
			t.Fatalf("unexpected delegated request: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer "+binding.Token || req.Header.Get("Copilot-Integration-Id") != copilotIntegrationID || req.Header.Get("Accept") != "application/json" {
			t.Fatal("delegated request missing expected caller or Copilot headers")
		}
		if empty {
			return copilotCatalogResponse(status, `{"data":[]}`), nil
		}
		return copilotCatalogResponse(status, fmt.Sprintf(`{"data":[{"id":"model-%d","name":"Model","vendor":"vendor","preview":true,"capabilities":{"limits":{"max_prompt_tokens":8000}}}]}`, calls)), nil
	})
	ctx := context.Background()
	for _, invalid := range []string{"", root + "-unbound", root + "-wrong-kind", root + "/child", root + "?x=1", root + "-shadowed"} {
		if _, _, err := CachedDelegatedCopilotModels(invalid); err == nil {
			t.Fatalf("cache read accepted %q", invalid)
		}
		if err := RefreshDelegatedCopilotModelsIfStale(ctx, invalid); err == nil {
			t.Fatalf("refresh accepted %q", invalid)
		}
	}
	assertCopilotDelegatedCache(t, root, "", false)
	if calls != 0 {
		t.Fatalf("cache reads or invalid refreshes made %d requests", calls)
	}
	for range 2 {
		if err := RefreshDelegatedCopilotModelsIfStale(ctx, " https://COPILOT-CATALOG.example.test:443/route/// "); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh made %d requests, want 1", calls)
	}
	assertCopilotDelegatedCache(t, root, "model-1", true)
	assertCopilotDelegatedCache(t, root+"-other", "", false)

	// An explicit live list must bypass the fresh cache and use the registered
	// caller even when the configured root needs normalization.
	provider, err := NewCopilotProviderWithBaseURL("", " "+root+"/ ")
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.ListModels(ctx)
	if err != nil || len(models) != 1 || models[0].ID != "model-2" {
		t.Fatalf("live models = %v, %v", models, err)
	}
	assertCopilotDelegatedCache(t, root, "model-2", true)
	if direct := GetCachedCopilotModels(); len(direct) != 1 || direct[0] != "direct" {
		t.Fatalf("direct catalog changed: %v", direct)
	}

	key := copilotDelegatedCatalogKey(binding)
	retagged := binding
	retagged.Token = caller + ".another-tag"
	if copilotDelegatedCatalogKey(retagged) != key {
		t.Fatal("attribution tag changed the caller's catalog scope")
	}
	otherCaller := binding
	otherCaller.Token = "sk-tlp-" + strings.Repeat("b", 64) + ".work"
	if _, err := cache.ReadModelCache(copilotDelegatedCatalogKey(otherCaller)); !os.IsNotExist(err) {
		t.Fatalf("another caller read this catalog: %v", err)
	}
	makeCopilotCatalogStale(t, cacheHome, key, caller)
	status = http.StatusUnauthorized
	if err := RefreshDelegatedCopilotModelsIfStale(ctx, root); err == nil {
		t.Fatal("unauthorized refresh succeeded")
	}
	assertCopilotDelegatedCache(t, root, "model-2", false)
	if calls != 3 {
		t.Fatalf("unauthorized refresh made more than one request: %d total", calls)
	}
	status = http.StatusOK
	if err := RefreshDelegatedCopilotModelsIfStale(ctx, root); err != nil {
		t.Fatal(err)
	}
	assertCopilotDelegatedCache(t, root, "model-4", true)
	empty = true
	if _, err := provider.ListModels(ctx); err != nil {
		t.Fatal(err)
	}
	assertCopilotDelegatedCache(t, root, "", true)
	if err := RefreshDelegatedCopilotModelsIfStale(ctx, root); err != nil || calls != 5 {
		t.Fatalf("fresh empty catalog should not refresh: %d requests, %v", calls, err)
	}
}

func assertCopilotDelegatedCache(t *testing.T, root, wantID string, wantFresh bool) {
	t.Helper()
	models, fresh, err := CachedDelegatedCopilotModels(root)
	if err != nil || fresh != wantFresh {
		t.Fatalf("catalog for %q = %v, %v, %v", root, models, fresh, err)
	}
	if wantID == "" && len(models) == 0 {
		return
	}
	if len(models) != 1 || models[0].ID != wantID || models[0].DisplayName != "Model (preview)" || models[0].OwnedBy != "vendor" || models[0].InputLimit != 8000 {
		t.Fatalf("catalog for %q = %+v, want %q with Copilot metadata", root, models, wantID)
	}
}

func makeCopilotCatalogStale(t *testing.T, cacheHome, key, caller string) {
	t.Helper()
	path := filepath.Join(cacheHome, "term-llm", key+"-models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, caller) || strings.Contains(string(data), caller) {
		t.Fatal("catalog persisted the raw caller secret")
	}
	var cached cache.ModelCache
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatal(err)
	}
	cached.FetchedAt = time.Now().Add(-cache.ModelCacheTTL - time.Minute)
	data, err = json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCopilotNativeAndCustomCatalogs(t *testing.T) {
	if !runProxyCatalogTestInChild(t) {
		return
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := cache.WriteModelCache("copilot", []string{"legacy-mixed"}); err != nil {
		t.Fatal(err)
	}
	if got := GetCachedCopilotModels(); len(got) != 0 {
		t.Fatalf("read legacy mixed catalog: %v", got)
	}
	const native = "https://api.business.githubcopilot.com"
	const custom = "https://custom-copilot.example.test/v1"
	exchanges := 0
	setCopilotCatalogTransport(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case copilotTokenURL:
			exchanges++
			if got := req.Header.Get("Authorization"); got != "token local-oauth" {
				t.Fatalf("token exchange authorization = %q", got)
			}
			return copilotCatalogResponse(http.StatusOK, fmt.Sprintf(`{"token":"session","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), native)), nil
		case native + "/models", custom + "/models":
			if got := req.Header.Get("Authorization"); got != "Bearer session" {
				t.Fatalf("models authorization = %q", got)
			}
			id := "native"
			if req.URL.String() == custom+"/models" {
				id = "custom"
			}
			return copilotCatalogResponse(http.StatusOK, fmt.Sprintf(`{"data":[{"id":%q}]}`, id)), nil
		default:
			t.Fatalf("unexpected request: %s", req.URL)
			return nil, nil
		}
	})
	provider := NewCopilotProviderWithCreds(&credentials.CopilotCredentials{AccessToken: "local-oauth"}, "")
	for _, tc := range []struct{ baseURL, want string }{{"", "native"}, {custom, "custom"}, {"", "native"}} {
		provider.SetBaseURL(tc.baseURL)
		models, err := provider.ListModels(context.Background())
		if err != nil || len(models) != 1 || models[0].ID != tc.want {
			t.Fatalf("live models for %q = %v, %v; want %s", tc.baseURL, models, err, tc.want)
		}
		if got := GetCachedCopilotModels(); len(got) != 1 || got[0] != "native" {
			t.Fatalf("direct catalog after listing %q = %v", tc.baseURL, got)
		}
	}
	if exchanges != 2 {
		t.Fatalf("token exchanges = %d, want 2", exchanges)
	}
}

func setCopilotCatalogTransport(t *testing.T, transport roundTripFunc) {
	t.Helper()
	old := copilotHTTPClient
	t.Cleanup(func() { copilotHTTPClient = old })
	copilotHTTPClient = &http.Client{Transport: transport}
}

func copilotCatalogResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}
