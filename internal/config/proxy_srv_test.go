package config

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

func stubProxySRV(t *testing.T, lookup func(string, string, string) (string, []*net.SRV, error)) {
	t.Helper()
	// These tests must stay sequential because the resolver is package-global.
	original := lookupSRV
	t.Cleanup(func() { lookupSRV = original })
	lookupSRV = lookup
}

func TestLoadProxySRVBindings(t *testing.T) {
	stubEmptyProxyDiscovery(t)
	const globalRecord = "_proxy._tcp.household.example.test"
	const nativeRecord = "_native._tcp.household.example.test"
	stubProxySRV(t, func(service, proto, name string) (string, []*net.SRV, error) {
		if service != "" || proto != "" {
			t.Fatalf("expected lookup of the full record name, got service=%q proto=%q", service, proto)
		}
		switch name {
		case globalRecord:
			return name, []*net.SRV{{Target: "srv-global.example.test.", Port: 8443}, {Target: "unused.example.test.", Port: 443}}, nil
		case nativeRecord:
			return name, []*net.SRV{{Target: "srv-native.example.test.", Port: 443}}, nil
		default:
			t.Fatalf("unexpected SRV lookup: %q", name)
			return "", nil, nil
		}
	})
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeTestConfig(t, home, `proxy:
  url: srv://`+globalRecord+`/gateway%20one/
  token: test-global-client-token
  providers: [openai]
providers:
  claude-bin:
    proxy:
      url: srv://`+nativeRecord+`/team/claude-bin/
      token: test-native-client-token
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	const globalRoot = "https://srv-global.example.test:8443/gateway%20one"
	const nativeRoot = "https://srv-native.example.test/team/claude-bin"
	if cfg.Proxy.URL != globalRoot || cfg.Image.OpenAI.BaseURL != unavailableProxyEndpoint(globalRoot, "openai") {
		t.Fatalf("expected resolved global and image roots, got %q and %q", cfg.Proxy.URL, cfg.Image.OpenAI.BaseURL)
	}
	openAI := cfg.Providers["openai"]
	if openAI.BaseURL != unavailableProxyEndpoint(globalRoot, "openai") || openAI.ProxyBinding != openAI.BaseURL {
		t.Fatalf("expected OpenAI to be bound under the discovered root, got %q (%q)", openAI.BaseURL, openAI.ProxyBinding)
	}
	native := cfg.Providers["claude-bin"]
	if native.Proxy == nil || native.Proxy.URL != "srv://"+nativeRecord+"/team/claude-bin/" || native.ProxyBinding != nativeRoot || native.Env["TERM_LLM_CLAUDE_PROXY_ROOT"] != nativeRoot {
		t.Fatal("native provider did not retain the SRV reference and use the discovered route")
	}
	for _, want := range []proxyclient.Binding{
		{Root: openAI.BaseURL, Token: "test-global-client-token"},
		{Root: nativeRoot, Kind: "claude-bin", Token: "test-native-client-token"},
	} {
		if got, ok := proxyclient.Lookup(want.Root + "/operation"); !ok || got.Root != want.Root || got.Kind != want.Kind || got.Token != want.Token {
			t.Errorf("missing scoped binding under %s", want.Root)
		}
	}
	if err := Save(cfg); err == nil || !strings.Contains(err.Error(), "materialized proxy") {
		t.Fatalf("saving runtime roots must fail, got %v", err)
	}
}

func TestInactiveProxySRVSkipsLookup(t *testing.T) {
	stubProxySRV(t, func(_, _, _ string) (string, []*net.SRV, error) {
		t.Fatal("a disabled global proxy should not look up an SRV record")
		return "", nil, nil
	})
	const raw = "srv://_proxy._tcp.household.example.test"
	cfg := &Config{Proxy: ProxyConfig{URL: raw, Providers: []string{}}}
	if err := cfg.materializeProxy(nil); err != nil || cfg.Proxy.URL != raw {
		t.Fatalf("inactive proxy lost its discovery URL or failed: %q, %v", cfg.Proxy.URL, err)
	}
}

func TestUnboundProxySRVRetainsSource(t *testing.T) {
	stubEmptyProxyDiscovery(t)
	stubProxySRV(t, func(_, _, name string) (string, []*net.SRV, error) {
		return name, []*net.SRV{{Target: "unused.example.test.", Port: 443}}, nil
	})
	const raw = "srv://_proxy._tcp.household.example.test"
	enabled := false
	cfg := &Config{
		Proxy:     ProxyConfig{URL: raw, Token: "test-global-client-token", Providers: []string{"anthropic"}},
		Providers: map[string]ProviderConfig{"anthropic": {Type: ProviderTypeAnthropic, Enabled: &enabled}},
	}
	if err := cfg.materializeProxy(nil); err != nil || cfg.Proxy.URL != raw || cfg.Proxy.materialized {
		t.Fatalf("unbound proxy must retain the source URL, got %q, %v", cfg.Proxy.URL, err)
	}
}

func TestResolveProxySRVPreservesBasePath(t *testing.T) {
	stubProxySRV(t, func(_, _, name string) (string, []*net.SRV, error) {
		return name, []*net.SRV{{Target: "target.example.test.", Port: 8443}}, nil
	})
	const path = "/base%20path/opaque%2Fid//"
	got, err := ResolveProxySRV("srv://_proxy._tcp.example.test" + path)
	if err != nil || got != "https://target.example.test:8443"+path {
		t.Fatalf("SRV endpoint = %q, err %v; want HTTPS target with original escaped path", got, err)
	}
}

func TestResolveProxySRVRejectsUnsafeReferencesBeforeDNS(t *testing.T) {
	stubProxySRV(t, func(_, _, _ string) (string, []*net.SRV, error) {
		t.Fatal("unsafe SRV URL must not trigger a DNS lookup")
		return "", nil, nil
	})
	const authority = "_proxy._tcp.household.example.test"
	for _, raw := range []string{
		"srv:///route", "srv:opaque", "srv://household.example.test", "srv://_proxy._udp.example.test",
		"srv://" + authority + ":8443", "srv://private:secret@" + authority,
		"srv://" + authority + "/route?", "srv://" + authority + "/route?x=y",
		"srv://" + authority + "/route#", "srv://" + authority + "/route#part",
		"srv://" + authority + "/route/%2e%2e/other", "srv://" + authority + "/route/%5cother",
	} {
		if got, err := ResolveProxySRV(raw); err == nil || got != "" {
			t.Errorf("unsafe reference %q resolved to %q (err %v)", raw, got, err)
		}
	}
}

func TestProxySRVFailedLookupFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []*net.SRV
		err     error
		want    string
	}{
		{name: "DNS error", err: errors.New("resolver unavailable"), want: "SRV lookup failed"},
		{name: "no records", want: "no SRV records"},
		{name: "unavailable service", answers: []*net.SRV{{Target: ".", Port: 443}}, want: "no usable target or port"},
		{name: "zero port", answers: []*net.SRV{{Target: "host.example.test."}}, want: "no usable target or port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubProxySRV(t, func(_, _, _ string) (string, []*net.SRV, error) { return "", tc.answers, tc.err })
			const raw = "srv://_proxy._tcp.household.example.test"
			cfg := &Config{Proxy: ProxyConfig{URL: raw, Providers: []string{"openai"}}}
			if err := cfg.materializeProxy(nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected failed discovery to reject proxy configuration with %q, got %v", tc.want, err)
			}
			if cfg.Proxy.URL != raw || len(cfg.Providers) != 0 {
				t.Fatal("failed discovery materialized proxy providers")
			}
		})
	}
}
