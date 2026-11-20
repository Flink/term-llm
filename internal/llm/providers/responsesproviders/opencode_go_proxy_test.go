package responsesproviders

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestScopedOpenCodeGoRoutesEveryNativeProtocolWithItsOwnAuth(t *testing.T) {
	for _, name := range []string{"opencode-go", "household"} {
		for _, separator := range []string{"/", "-"} {
			t.Run(name+separator, func(t *testing.T) {
				testScopedOpenCodeGoProtocols(t, name, separator)
			})
		}
	}
}

func testScopedOpenCodeGoProtocols(t *testing.T, name, separator string) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	upstream, _ := newOpenCodeGoTestServer(t)
	defer upstream.Close()
	type observation struct {
		path   string
		header http.Header
	}
	var mu sync.Mutex
	observed := []observation{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		observed = append(observed, observation{r.URL.Path, r.Header.Clone()})
		mu.Unlock()
		for _, prefix := range []string{"/" + name + separator + "messages", "/" + name + separator + "gemini", "/" + name} {
			if strings.HasPrefix(r.URL.Path, prefix+"/") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
				break
			}
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	root := gateway.URL + "/" + name
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Token: "scoped-family", RelatedRoots: map[string]string{"opencode-go-messages": root + separator + "messages", "opencode-go-gemini": root + separator + "gemini"}}, {Root: root + separator + "messages", Token: "scoped-family"}, {Root: root + separator + "gemini", Token: "scoped-family"}}); err != nil {
		t.Fatal(err)
	}
	provider := newOpenCodeGoProvider("scoped-family", "", root, "", gateway.Client())
	provider.catalog.store(map[string]opencodeGoModel{
		"chat-model":      {ModelInfo: ModelInfo{ID: "chat-model"}, Protocol: opencodeGoProtocolChat, Available: true},
		"messages-model":  {ModelInfo: ModelInfo{ID: "messages-model"}, Protocol: opencodeGoProtocolMessages, Available: true},
		"responses-model": {ModelInfo: ModelInfo{ID: "responses-model"}, Protocol: opencodeGoProtocolResponses, Available: true},
		"gemini-model":    {ModelInfo: ModelInfo{ID: "gemini-model"}, Protocol: opencodeGoProtocolGemini, Available: true},
	}, time.Now(), time.Now(), nil)
	cases := []struct{ model, path, header, value string }{
		{"chat-model", "/" + name + "/chat/completions", "Authorization", "Bearer scoped-family"},
		{"messages-model", "/" + name + separator + "messages/v1/messages", "x-api-key", "scoped-family"},
		{"responses-model", "/" + name + "/responses", "Authorization", "Bearer scoped-family"},
		{"gemini-model", "/" + name + separator + "gemini/models/gemini-model:streamGenerateContent", "x-goog-api-key", "scoped-family"},
	}
	for _, tc := range cases {
		drainOpenCodeGoStream(t, mustOpenCodeGoStream(t, provider, Request{Model: tc.model, Messages: []Message{UserText("synthetic protocol fixture")}}))
		mu.Lock()
		got := observed[len(observed)-1]
		mu.Unlock()
		if got.path != tc.path || got.header.Get(tc.header) != tc.value {
			t.Fatalf("%s used path %s / wrong scoped auth", tc.model, got.path)
		}
	}
}

func TestScopedOpenCodeGoMissingProtocolBindingFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bindings []proxyclient.Binding
		missing  string
	}{
		{name: "both", missing: "Messages"},
		{name: "messages", bindings: []proxyclient.Binding{{Root: "/household/gemini", Token: "scoped"}}, missing: "Messages"},
		{name: "gemini", bindings: []proxyclient.Binding{{Root: "/household/messages", Token: "scoped"}}, missing: "Gemini"},
		{name: "other-namespace", bindings: []proxyclient.Binding{{Root: "/opencode-go/messages", Token: "scoped"}, {Root: "/opencode-go/gemini", Token: "scoped"}}, missing: "Messages"},
		{name: "other-principal", bindings: []proxyclient.Binding{{Root: "/household/messages", Token: "other"}, {Root: "/household/gemini", Token: "other"}}, missing: "Messages"},
		{name: "other-kind", bindings: []proxyclient.Binding{{Root: "/household/messages", Token: "scoped", Kind: "other"}, {Root: "/household/gemini", Token: "scoped", Kind: "other"}}, missing: "Messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := "https://opencode-missing-" + tc.name + ".example.test"
			root := origin + "/household"
			bindings := []proxyclient.Binding{{Root: root, Token: "scoped", RelatedRoots: map[string]string{"opencode-go-messages": root + "/messages", "opencode-go-gemini": root + "/gemini"}}}
			for _, b := range tc.bindings {
				b.Root = origin + b.Root
				bindings = append(bindings, b)
			}
			if err := proxyclient.Register(bindings); err != nil {
				t.Fatal(err)
			}
			messages, gemini, err := openCodeGoProtocolRoots(root)
			if err == nil || !strings.Contains(err.Error(), tc.missing) || messages != "" || gemini != "" {
				t.Fatalf("missing protocol root did not fail closed: %q, %q, %v", messages, gemini, err)
			}
			provider := NewOpenCodeGoProviderWithBaseURL("scoped", "", root)
			for _, model := range []string{"chat-model", "messages-model", "responses-model", "gemini-model"} {
				if _, err := provider.Stream(context.Background(), Request{Model: model}); err == nil || !strings.Contains(err.Error(), "no direct fallback") {
					t.Fatalf("%s did not fail before sending a request: %v", model, err)
				}
			}
		})
	}
}

func TestScopedOpenCodeGoUsesExplicitProtocolRoots(t *testing.T) {
	root := "https://opencode-precedence.example.test/household"
	bindings := []proxyclient.Binding{{Root: root, Token: "scoped", RelatedRoots: map[string]string{"opencode-go-messages": root + "/messages", "opencode-go-gemini": root + "/gemini"}}}
	for _, suffix := range []string{"/messages", "/gemini", "-messages", "-gemini"} {
		bindings = append(bindings, proxyclient.Binding{Root: root + suffix, Token: "scoped"})
	}
	if err := proxyclient.Register(bindings); err != nil {
		t.Fatal(err)
	}
	messages, gemini, err := openCodeGoProtocolRoots(root)
	if err != nil || messages != root+"/messages" || gemini != root+"/gemini" {
		t.Fatalf("nested roots not preferred: %q, %q, %v", messages, gemini, err)
	}
}

func TestUnscopedOpenCodeGoKeepsDirectRoot(t *testing.T) {
	root := "https://opencode-direct.example.test/custom"
	messages, gemini, err := openCodeGoProtocolRoots(root)
	if err != nil || messages != root || gemini != root {
		t.Fatalf("direct roots changed: %q, %q, %v", messages, gemini, err)
	}
}

func TestScopedOpenCodeGoCannotBorrowAncestorProtocolRoots(t *testing.T) {
	root := "https://opencode-ancestor.example.test/shared"
	bindings := []proxyclient.Binding{{Root: root, Token: "scoped", RelatedRoots: map[string]string{"opencode-go-messages": root + "/messages", "opencode-go-gemini": root + "/gemini"}}}
	for _, suffix := range []string{"/messages", "/gemini", "-messages", "-gemini"} {
		bindings = append(bindings, proxyclient.Binding{Root: root + suffix, Token: "scoped"})
	}
	if err := proxyclient.Register(bindings); err != nil {
		t.Fatal(err)
	}
	messages, gemini, err := openCodeGoProtocolRoots(root + "/household")
	if err == nil || !strings.Contains(err.Error(), "not explicitly registered") || messages != "" || gemini != "" {
		t.Fatalf("named provider borrowed its ancestor's protocol roots: %q, %q, %v", messages, gemini, err)
	}
}
