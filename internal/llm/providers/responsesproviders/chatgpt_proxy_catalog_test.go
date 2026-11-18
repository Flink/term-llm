package responsesproviders

import (
	"context"
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

func TestChatGPTProxyCatalogSharedAcrossProvidersAndScopedToRoute(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const origin = "https://chatgpt-catalog-cache.example.test"
	const first = origin + "/first"
	const second = origin + "/second"
	if err := proxyclient.Register([]proxyclient.Binding{
		{Root: first, Kind: "chatgpt", Token: "test-scoped-proxy-token"},
		{Root: second, Kind: "chatgpt", Token: "test-scoped-proxy-token"},
	}); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	origClient := chatGPTHTTPClient
	t.Cleanup(func() { chatGPTHTTPClient = origClient })
	chatGPTHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		root := strings.TrimSuffix(req.URL.Path, "/models")
		calls[root]++
		model := fmt.Sprintf("%s-%d", strings.TrimPrefix(root, "/"), calls[root])
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"models":[{"slug":%q,"supported_reasoning_levels":["low"]}]}`, model)))}, nil
	})}
	provider := func(root string) *ChatGPTProvider {
		t.Helper()
		creds, err := credentials.GetChatGPTCredentialsForEndpoint(root)
		if err != nil || !creds.Delegated() {
			t.Fatalf("expected delegated credentials, got %v", err)
		}
		return NewChatGPTProviderWithCredsAndOptions(creds, "", ChatGPTProviderOptions{BaseURL: root})
	}
	ctx := context.Background()
	for _, root := range []string{first, second} {
		key := root + "\x00" + provider(root).creds.AccountID
		t.Cleanup(func() {
			proxyChatGPTCatalogs.Lock()
			delete(proxyChatGPTCatalogs.entries, key)
			proxyChatGPTCatalogs.Unlock()
		})
	}
	assertCatalog := func(root, want string) []ModelInfo {
		t.Helper()
		models, err := provider(root).ListModelsForProvider(ctx, "chatgpt")
		if err != nil || len(models) != 1 || models[0].ID != want {
			t.Fatalf("got %v, %v; want %s", models, err, want)
		}
		return models
	}

	models := assertCatalog(first, "first-1")
	models[0].ReasoningEfforts[0] = "modified"
	if got := assertCatalog(first, "first-1")[0].ReasoningEfforts[0]; got != "low" {
		t.Fatalf("a caller mutated cached metadata: %s", got)
	}
	assertCatalog(second, "second-1")
	assertCatalog(first, "first-1")
	if calls["/first"] != 1 || calls["/second"] != 1 {
		t.Fatalf("expected one catalog request per route: %v", calls)
	}
	refreshed, err := provider(first).RefreshModelsForProvider(ctx, "chatgpt")
	if err != nil || len(refreshed) != 1 || refreshed[0].ID != "first-2" {
		t.Fatalf("forced refresh did not bypass cache: %v, %v", refreshed, err)
	}
	assertCatalog(first, "first-2")

	key := first + "\x00" + provider(first).creds.AccountID
	proxyChatGPTCatalogs.Lock()
	entry := proxyChatGPTCatalogs.entries[key]
	entry.FetchedAt = time.Now().Add(-chatGPTModelsCacheTTL - time.Second)
	proxyChatGPTCatalogs.entries[key] = entry
	proxyChatGPTCatalogs.Unlock()
	assertCatalog(first, "first-3")
	path, err := chatGPTModelsCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("delegated facts must not populate the direct account's disk cache: %v", err)
	}
}
