package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestProxyTagMaterializesGlobalAndNativeBindings(t *testing.T) {
	resetProxyDiscoveryCache()
	t.Cleanup(resetProxyDiscoveryCache)
	secret := "sk-tlp-" + strings.Repeat("a", 64)
	nativeSecret := "sk-tlp-" + strings.Repeat("b", 64)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_proxy/client/v2/providers" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+secret+".first" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"revision":1,"providers":[{"id":"exa","family":"search","routes":[{"name":"exa","local_prefix":"/exa/","profile":"exa"}]}]}`)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("TAG_TEST_PROXY_TOKEN", secret)
	t.Setenv("TAG_TEST_NATIVE_TOKEN", nativeSecret)
	for _, tag := range []string{"first", "second"} {
		writeTestConfig(t, home, `proxy:
  url: `+srv.URL+`
  token: ${TAG_TEST_PROXY_TOKEN}
  tag: `+tag+`
  providers: [exa]
providers:
  claude-bin:
    proxy:
      url: https://tag-native.example.test/claude-bin/
      token: ${TAG_TEST_NATIVE_TOKEN}
`)
		cfg, err := LoadWithoutCredentials()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Proxy.Tag != tag || cfg.Proxy.Token != "${TAG_TEST_PROXY_TOKEN}" {
			t.Fatal("materialization did not preserve the tag and credential reference")
		}
		for root, want := range map[string]string{
			srv.URL + "/exa/": secret + "." + tag,
			"https://tag-native.example.test/claude-bin": nativeSecret + "." + tag,
		} {
			if binding, ok := proxyclient.Lookup(root); !ok || binding.Token != want {
				t.Fatalf("missing tagged binding for %s", root)
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatal("changing the tag should not invalidate discovery for the same caller")
	}
}

func TestProxyTagOnlyConfigRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeTestConfig(t, home, "proxy:\n  tag: laptop-1\n")
	cfg, err := LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy.Tag != "laptop-1" || cfg.Proxy.URL != "" {
		t.Fatal("tag-only config did not load without a proxy URL")
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Proxy.Tag != cfg.Proxy.Tag {
		t.Fatal("saving a tag-only config lost the tag")
	}
}

func TestProxyTagConfigErrors(t *testing.T) {
	for _, tc := range []struct{ name, config, errorPart string }{
		{"invalid label", "proxy:\n  tag: -bad\n", "proxy.tag must be"},
		{"legacy token", "proxy:\n  url: https://tag-check.example.test\n  token: not-a-canonical-client-secret\n  tag: laptop\n", "proxy.tag requires"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			writeTestConfig(t, home, tc.config)
			_, err := LoadWithoutCredentials()
			if err == nil || !strings.Contains(err.Error(), tc.errorPart) || strings.Contains(err.Error(), "not-a-canonical-client-secret") {
				t.Fatal("tag configuration did not fail safely")
			}
		})
	}
}
