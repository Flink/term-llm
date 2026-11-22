package responsesproviders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

func forgetChatGPTProxyCatalog(scope delegatedChatGPTCatalogScope) {
	proxyChatGPTCatalogs.Lock()
	delete(proxyChatGPTCatalogs.entries, scope.id)
	proxyChatGPTCatalogs.Unlock()
}

func TestDelegatedChatGPTCatalogShell(t *testing.T) {
	if !runProxyCatalogTestInChild(t) {
		return
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	secret := "sk-tlp-" + strings.Repeat("a", 64)
	var calls, status atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/route/models" || r.URL.Query().Get("client_version") != chatGPTModelsClientVersion || r.Header.Get("Authorization") != "Bearer "+secret+".first" {
			t.Error("catalog did not use the expected delegated request")
		}
		if status.Load() != 0 {
			w.WriteHeader(int(status.Load()))
			return
		}
		fmt.Fprint(w, `{"models":[{"slug":"private-model","display_name":"Private","context_window":1234,"max_context_window":5678,"service_tiers":[{"id":"priority","description":"Fast service"}],"additional_speed_tiers":["fast"],"supported_reasoning_levels":["low","high"],"default_reasoning_level":"high"}]}`)
	}))
	defer server.Close()
	root := server.URL + "/route"
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: "chatgpt", Token: secret + ".first"}}); err != nil {
		t.Fatal(err)
	}
	if err := saveChatGPTModelsCache(chatGPTModelsCache{
		AccountID: "direct-account", ClientVersion: chatGPTModelsClientVersion,
		FetchedAt: time.Now(), Models: []ModelInfo{{ID: "direct-only"}},
	}); err != nil {
		t.Fatal(err)
	}
	if models, _, err := CachedChatGPTModels(); !errors.Is(err, os.ErrNotExist) || len(models) != 0 {
		t.Fatalf("unscoped catalog exposed direct account data with delegation active: %v, %v", models, err)
	}
	scope, binding, err := delegatedChatGPTCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	defer forgetChatGPTProxyCatalog(scope)
	if models, fresh, err := CachedDelegatedChatGPTModels(root); models != nil || fresh || err != nil || calls.Load() != 0 {
		t.Fatalf("cold cache = %v, %v, %v; calls = %d", models, fresh, err, calls.Load())
	}
	if err := RefreshDelegatedChatGPTModelsIfStale(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	forgetChatGPTProxyCatalog(scope)
	want := []ModelInfo{{ID: "private-model", DisplayName: "Private", InputLimit: 1234, BackendContext: 1234, MaxContext: 5678, InputPrice: -1, OutputPrice: -1,
		ServiceTiers: []ModelServiceTier{{ID: "priority", Description: "Fast service"}}, AdditionalSpeedTiers: []string{"fast"}, ReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: "high"}}
	assertCache := func(fresh bool) {
		t.Helper()
		if got, f, err := CachedDelegatedChatGPTModels(root + "/"); err != nil || f != fresh || !reflect.DeepEqual(got, want) {
			t.Fatalf("cache = %+v, %v, %v; want %+v, %v, nil", got, f, err, want, fresh)
		}
	}
	assertCache(true)
	binding.Token = secret + ".second"
	if err := proxyclient.Register([]proxyclient.Binding{binding}); err != nil {
		t.Fatal(err)
	}
	assertCache(true) // Attribution tags do not change the caller's scope.
	// Registration is immutable across callers within a process. Compute the
	// scope a second invocation with a different token would use.
	otherCaller := binding
	otherCaller.Token = "sk-tlp-" + strings.Repeat("b", 64)
	if _, err := chatGPTCatalogScopeForBinding(otherCaller).load(); !os.IsNotExist(err) {
		t.Fatalf("another caller could read the cache: %v", err)
	}
	if err := RefreshDelegatedChatGPTModelsIfStale(context.Background(), root); err != nil || calls.Load() != 1 {
		t.Fatalf("fresh refresh = %v; calls = %d", err, calls.Load())
	}
	binding.Token = secret + ".first"
	if err := proxyclient.Register([]proxyclient.Binding{binding}); err != nil {
		t.Fatal(err)
	}
	entry, err := scope.load()
	if err != nil {
		t.Fatal(err)
	}
	entry.FetchedAt = time.Now().Add(-chatGPTModelsCacheTTL - time.Second)
	if err := scope.save(entry); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusUnauthorized)
	if err := RefreshDelegatedChatGPTModelsIfStale(context.Background(), root); err == nil || calls.Load() != 2 {
		t.Fatalf("unauthorized refresh = %v; calls = %d", err, calls.Load())
	}
	assertCache(false)
	status.Store(0)
	if err := RefreshDelegatedChatGPTModelsIfStale(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	assertCache(true)
	path, err := scope.path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, secret) || strings.Contains(string(data), secret) || strings.Contains(string(data), root) {
		t.Fatal("cache leaked binding information")
	}
}

func TestDelegatedChatGPTCatalogRejectsNonExactBindings(t *testing.T) {
	if !runProxyCatalogTestInChild(t) {
		return
	}
	const host = "https://chatgpt-cache-binding.example.test"
	if err := proxyclient.Register([]proxyclient.Binding{
		{Root: host + "/valid", Kind: "chatgpt", Token: "one"},
		{Root: host + "/wrong", Kind: "not-chatgpt", Token: "one"},
		{Root: host + "/shadowed", Kind: "chatgpt", Token: "one"},
		{Root: host + "/shadowed/models", Kind: "chatgpt", Token: "two"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/missing", "/valid/child", "/valid?other=true", "/wrong", "/shadowed"} {
		if _, _, err := CachedDelegatedChatGPTModels(host + path); !errors.Is(err, errChatGPTCatalogBinding) {
			t.Errorf("cache for %s: %v", path, err)
		}
		if err := RefreshDelegatedChatGPTModelsIfStale(context.Background(), host+path); !errors.Is(err, errChatGPTCatalogBinding) {
			t.Errorf("refresh for %s: %v", path, err)
		}
	}
}

func TestDelegatedChatGPTCatalogRefreshHonorsCancellation(t *testing.T) {
	if !runProxyCatalogTestInChild(t) {
		return
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	started, canceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer srv.Close()
	if err := proxyclient.Register([]proxyclient.Binding{{Root: srv.URL, Kind: "chatgpt", Token: "token"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- RefreshDelegatedChatGPTModelsIfStale(ctx, srv.URL) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refresh error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not return when canceled")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("network request outlived caller cancellation")
	}
	if models, fresh, err := CachedDelegatedChatGPTModels(srv.URL); models != nil || fresh || err != nil {
		t.Fatalf("canceled refresh wrote a cache: %v, %v, %v", models, fresh, err)
	}
}
