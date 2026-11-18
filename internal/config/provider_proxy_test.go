package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
	"gopkg.in/yaml.v3"
)

func TestProviderProxyLoadOnlyOptsInNativeClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	token := strings.Repeat("local-scoped-", 3)
	t.Setenv("CONFIG_TEST_CLAUDE_PROXY_TOKEN", token)
	marker := filepath.Join(home, "must-not-exist")
	writeTestConfig(t, home, `default_provider: claude-bin
providers:
  claude-bin:
    proxy:
      url: https://LOCAL-CLAUDE.example.test:443/team/claude/
      token: ${CONFIG_TEST_CLAUDE_PROXY_TOKEN}
    api_key: $(touch `+marker+`)
    env:
      CUSTOM_CLAUDE_OPTION: keep-this
      ANTHROPIC_API_KEY: $(touch `+marker+`)
  regular-claude:
    type: claude-bin
    env:
      UNRELATED: keep-me
  anthropic:
    api_key: untouched-local-key
    base_url: https://direct-anthropic.example.test
  openai:
    api_key: another-local-key
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.GetProviderConfig("claude-bin")
	root := "https://local-claude.example.test/team/claude"
	if pc == nil || pc.Type != ProviderTypeClaudeBin || pc.ProxyBinding != root || pc.BaseURL != root {
		t.Fatal("provider-local route was not materialized")
	}
	if pc.Env["TERM_LLM_CLAUDE_PROXY_ROOT"] != root || pc.Env["CUSTOM_CLAUDE_OPTION"] != "keep-this" || pc.APIKey != "" || pc.ResolvedAPIKey != "" {
		t.Fatal("runtime config retained local credentials or missed the launcher marker")
	}
	binding, ok := proxyclient.Lookup(root + "/v1/messages")
	if !ok || binding.Root != root || binding.Kind != "claude-code" || binding.Token != token {
		t.Fatal("missing scoped native Claude binding")
	}
	if err := pc.ResolveForInference(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("local credential command executed")
	}
	if regular := cfg.GetProviderConfig("regular-claude"); regular.ProxyBinding != "" || regular.Env["UNRELATED"] != "keep-me" {
		t.Fatal("regular Claude configuration changed")
	}
	if p := cfg.GetProviderConfig("anthropic"); p.ProxyBinding != "" || p.APIKey != "untouched-local-key" || p.BaseURL != "https://direct-anthropic.example.test" {
		t.Fatal("provider-local opt-in changed Anthropic configuration")
	}
	if p := cfg.GetProviderConfig("openai"); p.ProxyBinding != "" || p.APIKey != "another-local-key" {
		t.Fatal("provider-local opt-in changed another provider")
	}
	data, err := yaml.Marshal(pc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) || pc.Proxy.Token != "${CONFIG_TEST_CLAUDE_PROXY_TOKEN}" {
		t.Fatal("resolved token escaped the binding registry")
	}
}

func TestProviderProxyValidatesResolvedType(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind ProviderType
		want bool
	}{
		{name: "claude-bin", want: true},
		{name: "native-alias", kind: ProviderTypeClaudeBin, want: true},
		{name: "claude-bin", kind: ProviderTypeAnthropic},
		{name: "anthropic"},
		{name: "custom-without-type"},
		{name: "native-alias", kind: ProviderTypeOpenAICompat},
	} {
		t.Run(tc.name+"/"+string(tc.kind), func(t *testing.T) {
			root := "https://type-" + tc.name + ".example.test/route"
			cfg := Config{Providers: map[string]ProviderConfig{tc.name: {Type: tc.kind, Proxy: &ProviderProxyConfig{URL: root, Token: strings.Repeat("v", 32)}}}}
			err := cfg.materializeProxy(nil)
			if tc.want {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.GetProviderConfig(tc.name).ProxyBinding != root {
					t.Fatal("alias lost its binding")
				}
			} else if err == nil || !strings.Contains(err.Error(), "only supported for type claude-bin") {
				t.Fatalf("expected resolved-type rejection, got %v", err)
			}
		})
	}
}

func TestProviderProxyInvalidSettingsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy ProviderProxyConfig
		field string
	}{
		{name: "empty", field: "proxy"},
		{name: "missing url", proxy: ProviderProxyConfig{Token: strings.Repeat("t", 32)}, field: "proxy"},
		{name: "missing token", proxy: ProviderProxyConfig{URL: "https://missing-token.example.test/route"}, field: "proxy.token"},
		{name: "empty env", proxy: ProviderProxyConfig{URL: "https://empty-token.example.test/route", Token: "${CONFIG_TEST_MISSING_PROXY_TOKEN}"}, field: "proxy.token"},
		{name: "short token", proxy: ProviderProxyConfig{URL: "https://short-token.example.test/route", Token: "short"}, field: "proxy.token"},
		{name: "whitespace token", proxy: ProviderProxyConfig{URL: "https://space-token.example.test/route", Token: "scoped token with spaces"}, field: "proxy.token"},
		{name: "admin token", proxy: ProviderProxyConfig{URL: "https://admin-token.example.test/route", Token: "admin_private-admin-session-token"}, field: "proxy.token"},
		{name: "vendor token", proxy: ProviderProxyConfig{URL: "https://vendor-token.example.test/route", Token: "sk-ant-oat01-real-vendor-token"}, field: "proxy.token"},
		{name: "colon token", proxy: ProviderProxyConfig{URL: "https://colon-token.example.test/route", Token: "scoped:token-long-enough"}, field: "proxy.token"},
		{name: "unicode whitespace", proxy: ProviderProxyConfig{URL: "https://unicode-token.example.test/route", Token: "scoped\u00a0token-long-enough"}, field: "proxy.token"},
		{name: "control character", proxy: ProviderProxyConfig{URL: "https://control-token.example.test/route", Token: "scoped\x00token-long-enough"}, field: "proxy.token"},
		{name: "resolver error", proxy: ProviderProxyConfig{URL: "https://error-token.example.test/route", Token: "file:///must-not-exist/config-secret-token"}, field: "proxy.token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONFIG_TEST_MISSING_PROXY_TOKEN", "")
			t.Setenv("ANTHROPIC_API_KEY", strings.Repeat("local-fallback-key", 2))
			cfg := Config{Providers: map[string]ProviderConfig{"claude-bin": {Proxy: &tc.proxy, APIKey: strings.Repeat("local-api-key", 2)}}}
			err := cfg.materializeProxy(nil)
			if err == nil || !strings.Contains(err.Error(), "providers.claude-bin."+tc.field) {
				t.Fatalf("expected provider-local configuration error, got %v", err)
			}
			if tc.proxy.Token != "" && strings.Contains(err.Error(), tc.proxy.Token) {
				t.Fatal("credential reference leaked in error")
			}
			if _, ok := proxyclient.Lookup(tc.proxy.URL); ok {
				t.Fatal("invalid config registered a binding")
			}
		})
	}
}

func TestProviderProxyRootValidation(t *testing.T) {
	for _, raw := range []string{
		"", "https://proxy.example.test", "https://proxy.example.test/",
		"http://remote.example.test/route", "ftp://localhost/route",
		"https://user:private@proxy.example.test/route", "https://proxy.example.test/route?private=value",
		"https://proxy.example.test/route#private", "https://proxy.example.test/../route",
		"https://proxy.example.test/a/./route", "https://proxy.example.test/a//route",
		"https://proxy.example.test/a%2froute", "https://proxy.example.test/%2e%2e/route",
		"https://proxy.example.test/a%5croute", "https://proxy.example.test:99999/route",
		"https://proxy.example.test:/route", "https://proxy.example.test:0/route",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := providerProxyRoot(raw); err == nil {
				t.Fatal("accepted an invalid provider proxy URL")
			}
		})
	}
	for raw, want := range map[string]string{
		"https://PROXY.example.test:443/prefix/route/": "https://proxy.example.test/prefix/route",
		"http://localhost:80/route":                    "http://localhost/route",
		"http://127.0.0.1:1234/route/":                 "http://127.0.0.1:1234/route",
		"http://[::1]:1234/route":                      "http://[::1]:1234/route",
		"https://proxy.example.test:8443/route":        "https://proxy.example.test:8443/route",
	} {
		if got, err := providerProxyRoot(raw); err != nil || got != want {
			t.Fatalf("canonical route root = %q, %v; want %q", got, err, want)
		}
	}
}

func TestProviderProxyRejectsGlobalConflict(t *testing.T) {
	for _, global := range []ProxyConfig{
		{URL: "https://global-conflict.example.test", Token: strings.Repeat("g", 32)},
		{URL: "https://global-conflict.example.test", Token: strings.Repeat("g", 32), Direct: []string{"claude-bin"}},
		{Token: strings.Repeat("g", 32)},
		{Direct: []string{"claude-bin"}},
	} {
		cfg := Config{Proxy: global, Providers: map[string]ProviderConfig{"claude-bin": {Proxy: &ProviderProxyConfig{URL: "https://local-conflict.example.test/route", Token: strings.Repeat("l", 32)}}}}
		if err := cfg.materializeProxy(nil); err == nil || !strings.Contains(err.Error(), "cannot be combined with global proxy settings") {
			t.Fatalf("expected unambiguous conflict error, got %v", err)
		}
		if _, ok := proxyclient.Lookup("https://local-conflict.example.test/route"); ok {
			t.Fatal("conflicting local config registered a binding")
		}
	}
}

func TestProviderProxyUsesSeparateEnrolledClaudeBinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	const root = "https://combined-proxy.example.test"
	const shared = "test-shared-client-token"
	const enrolled = "test-enrolled-claude-client-token"
	writeTestConfig(t, home, `proxy:
  url: `+root+`
  token: `+shared+`
  providers: [chatgpt, venice]
providers:
  claude-bin:
    proxy:
      url: `+root+`/claude-code/
      token: `+enrolled+`
`)
	cfg, err := LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ provider, path, token, kind string }{
		{"claude-bin", "/claude-code", enrolled, "claude-code"},
		{"chatgpt", "/chatgpt", shared, "chatgpt"},
		{"venice", "/venice", shared, ""},
	} {
		endpoint := root + tc.path
		pc := cfg.GetProviderConfig(tc.provider)
		b, ok := proxyclient.Lookup(endpoint)
		if pc == nil || pc.ProxyBinding != endpoint || !ok || b.Root != endpoint || b.Token != tc.token || b.Kind != tc.kind {
			t.Errorf("wrong scoped binding for %s", tc.provider)
		}
	}
	if cfg.GetProviderConfig("claude-bin").Env["TERM_LLM_CLAUDE_PROXY_ROOT"] != root+"/claude-code" {
		t.Fatal("Claude child did not get its proxy root")
	}
}

func TestProviderProxyMixedConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, localPath, selection, want string }{
		{"double-selection", "/claude-code", "claude-bin", `remove "claude-bin" from proxy.providers`},
		{"bad-selection", "/claude-code", "no-such-provider", "not a known provider"},
		{"same-root", "/venice", "venice", "conflicting credential kinds or principals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := "https://" + tc.name + ".mixed-proxy.example.test"
			cfg := Config{
				Proxy: ProxyConfig{URL: root, Token: "test-shared-client-token", Providers: []string{tc.selection}},
				Providers: map[string]ProviderConfig{"claude-bin": {Proxy: &ProviderProxyConfig{
					URL: root + tc.localPath, Token: "test-enrolled-claude-client-token",
				}}},
			}
			if err := cfg.materializeProxy(nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			for _, path := range []string{tc.localPath, "/venice", "/claude-code"} {
				if _, ok := proxyclient.Lookup(root + path); ok {
					t.Fatalf("failed config registered %s", path)
				}
			}
		})
	}
}

func TestProviderProxyDoesNotRegisterPartialConfiguration(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConfig{
		"valid":   {Type: ProviderTypeClaudeBin, Proxy: &ProviderProxyConfig{URL: "https://partial-local.example.test/route", Token: strings.Repeat("p", 32)}},
		"invalid": {Type: ProviderTypeClaudeBin, Proxy: &ProviderProxyConfig{URL: "https://invalid-local.example.test/route"}},
	}}
	if err := cfg.materializeProxy(nil); err == nil {
		t.Fatal("invalid sibling accepted")
	}
	if _, ok := proxyclient.Lookup(cfg.Providers["valid"].Proxy.URL); ok || cfg.Providers["valid"].ProxyBinding != "" {
		t.Fatal("failed configuration partially materialized")
	}
}

func TestProviderProxySchema(t *testing.T) {
	for _, path := range []string{"proxy", "proxy.url", "proxy.token"} {
		if !IsKnownKey("providers.claude-bin."+path) || !IsKnownKey("providers.native-alias."+path) {
			t.Fatalf("provider proxy schema is missing %s", path)
		}
	}
	for _, field := range ProviderKeySpecs() {
		if field.Path == "proxy.token" {
			if !field.Sensitive {
				t.Fatal("provider-local token must be sensitive")
			}
			return
		}
	}
	t.Fatal("provider-local token missing from field schema")
}
