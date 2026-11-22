package responsesproviders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/credentials"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const delegatedGrokTestRoot = "https://proxy.example.test/grok"
const delegatedGrokTestToken = "scoped-grok-test-token"

func TestDelegatedGrokProvider(t *testing.T) {
	if !runProxyCatalogTestInChild(t) {
		return
	}
	if err := proxyclient.Register([]proxyclient.Binding{
		{Root: delegatedGrokTestRoot, Kind: "grok", Token: delegatedGrokTestToken},
		{Root: delegatedGrokTestRoot + "-other", Kind: "grok", Token: delegatedGrokTestToken},
		{Root: delegatedGrokTestRoot + "-chatgpt", Kind: "chatgpt", Token: "not-grok"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Run("cache", testDelegatedGrokCatalog)
	t.Run("stream", testDelegatedGrokStream)
}

func setGrokCatalogTransport(t *testing.T, transport roundTripFunc) {
	t.Helper()
	old := grokHTTPClient
	t.Cleanup(func() { grokHTTPClient = old })
	grokHTTPClient = &http.Client{Transport: transport}
}

func testDelegatedGrokCatalog(t *testing.T) {
	isolateGrokLLMTestEnv(t)
	ctx := context.Background()
	baseURL := delegatedGrokTestRoot
	creds := &credentials.GrokCredentials{AccessToken: "local-access", RefreshToken: "local-refresh", AccountID: "local-account", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := credentials.SaveGrokCredentials(creds); err != nil {
		t.Fatal(err)
	}
	if err := saveGrokModelsCache(grokModelsCache{AccountID: creds.AccountID, Models: []ModelInfo{{ID: "direct"}}}); err != nil {
		t.Fatal(err)
	}

	calls, status := 0, http.StatusOK
	setGrokCatalogTransport(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.String() != baseURL+"/models" || req.Header.Get("Authorization") != "Bearer "+delegatedGrokTestToken {
			t.Fatalf("unexpected catalog request: %s %s, authorization %q", req.Method, req.URL, req.Header.Get("Authorization"))
		}
		if got := req.Header.Get("x-userid"); got == creds.AccountID {
			t.Fatal("delegated catalog sent local account ID")
		}
		body := fmt.Sprintf(`{"data":[{"id":"model-%d","api_backend":"responses"}]}`, calls)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	assertCached := func(root, want string, wantFresh bool) {
		t.Helper()
		models, fresh, err := CachedDelegatedGrokModels(root)
		if err != nil || fresh != wantFresh {
			t.Fatalf("cache at %q = %v, %v, %v", root, models, fresh, err)
		}
		if want == "" && len(models) == 0 || len(models) == 1 && models[0].ID == want {
			return
		}
		t.Fatalf("cache at %q = %v, want %q", root, models, want)
	}
	assertCached(baseURL, "", false)
	for _, root := range []string{"", baseURL + "-unbound", baseURL + "-chatgpt", baseURL + "/child"} {
		if err := RefreshDelegatedGrokModelsIfStale(ctx, root); err == nil {
			t.Fatalf("refresh accepted non-Grok binding %q", root)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid binding or cache read made %d network requests", calls)
	}
	for range 2 {
		if err := RefreshDelegatedGrokModelsIfStale(ctx, " "+baseURL+"/ "); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh made %d requests, want 1", calls)
	}
	assertCached(baseURL, "model-1", true)
	assertCached(baseURL+"-other", "", false)

	// Even with explicitly supplied local credentials, listing a registered
	// route must use its binding, bypass the fresh cache, and update it.
	provider := NewGrokProviderWithCredsAndOptions(creds, "", GrokProviderOptions{BaseURL: baseURL})
	models, fresh, err := provider.ListModelsWithFreshness(ctx)
	if err != nil || !fresh || len(models) != 1 || models[0].ID != "model-2" {
		t.Fatalf("explicit list = %v, %v, %v", models, fresh, err)
	}
	assertCached(baseURL, "model-2", true)
	if direct, err := loadGrokModelsCache(creds.AccountID); err != nil || direct.Models[0].ID != "direct" {
		t.Fatalf("direct cache changed: %+v, %v", direct, err)
	}

	scope, _, err := delegatedGrokCatalog(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.save(grokModelsCache{Models: models, FetchedAt: time.Now().Add(-grokModelsCacheTTL - time.Second)}); err != nil {
		t.Fatal(err)
	}
	path, err := scope.path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, delegatedGrokTestToken) || bytes.Contains(data, []byte(delegatedGrokTestToken)) {
		t.Fatal("cache persisted the caller token")
	}
	status = http.StatusUnauthorized
	if err := RefreshDelegatedGrokModelsIfStale(ctx, baseURL); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("refresh failure = %v, want HTTP 401", err)
	}
	if calls != 3 {
		t.Fatalf("unauthorized refresh retried: %d total requests", calls)
	}
	assertCached(baseURL, "model-2", false)
	status = http.StatusOK
	if err := RefreshDelegatedGrokModelsIfStale(ctx, baseURL); err != nil {
		t.Fatal(err)
	}
	assertCached(baseURL, "model-4", true)
}

func testDelegatedGrokStream(t *testing.T) {
	isolateGrokLLMTestEnv(t)
	ctx := context.Background()
	provider, err := NewGrokProviderWithOptions("new-grok-high", GrokProviderOptions{BaseURL: delegatedGrokTestRoot})
	if err != nil {
		t.Fatal(err)
	}
	type wireRequest struct {
		Model     string                  `json:"model"`
		MaxOutput int                     `json:"max_output_tokens"`
		Reasoning struct{ Effort string } `json:"reasoning"`
	}
	var captured wireRequest
	catalog := `{"data":[
		{"model":"new-grok","api_backend":"responses","max_completion_tokens":1234,"reasoning_efforts":[{"value":"high"},{"value":"low"}]},
		{"model":"new-grok-low","api_backend":"responses","max_completion_tokens":2345},
		{"model":"grok-4.5","api_backend":"responses"}
	]}`
	setGrokCatalogTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer "+delegatedGrokTestToken {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		switch req.URL.String() {
		case delegatedGrokTestRoot + "/models":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(catalog))}, nil
		case delegatedGrokTestRoot + "/responses":
			captured = wireRequest{}
			if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
				t.Fatal(err)
			}
			return grokSSE(`event: response.completed`, `data: {"type":"response.completed","response":{}}`, `data: [DONE]`), nil
		default:
			t.Fatalf("unexpected URL: %s", req.URL)
			return nil, nil
		}
	})
	for _, tc := range []struct {
		model, explicit, wantModel, wantEffort string
		wantLimit                              int
	}{
		{wantModel: "new-grok", wantEffort: "high", wantLimit: 1234},
		{model: "new-grok-low", wantModel: "new-grok-low", wantLimit: 2345},
		{model: "new-grok-high", explicit: "medium", wantModel: "new-grok", wantEffort: "medium", wantLimit: 1234},
		{model: "grok-4.5-high", wantModel: "grok-4.5-high", wantLimit: 3000},
		{model: "grok-4.6-high", wantModel: "grok-4.6", wantEffort: "high", wantLimit: 3000},
	} {
		stream, err := provider.Stream(ctx, Request{Model: tc.model, ReasoningEffort: tc.explicit, MaxOutputTokens: 3000, Messages: []Message{UserText("hello")}})
		if err != nil {
			t.Fatal(err)
		}
		drainStreamToDone(t, stream)
		if captured.Model != tc.wantModel || captured.Reasoning.Effort != tc.wantEffort || captured.MaxOutput != tc.wantLimit {
			t.Fatalf("request for %q = %+v; want model %q, effort %q, limit %d", tc.model, captured, tc.wantModel, tc.wantEffort, tc.wantLimit)
		}
	}
}
