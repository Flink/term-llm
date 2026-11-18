package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
	"gopkg.in/yaml.v3"
)

func TestProviderProxySaveRoundTripPreservesCredentialReference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	token := strings.Repeat("file-scoped-", 3)
	tokenPath := filepath.Join(home, "scoped-token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	reference := "file://" + tokenPath
	cfg := &Config{DefaultProvider: "native-alias", Providers: map[string]ProviderConfig{
		"native-alias": {Type: ProviderTypeClaudeBin, Model: "claude-sonnet-5-5", Proxy: &ProviderProxyConfig{URL: "https://save-local.example.test/team/native/", Token: reference}},
		"claude-bin":   {Model: "claude-opus-5-5"},
	}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	path, err := GetConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), reference) || strings.Contains(string(before), token) || strings.Contains(string(before), "TERM_LLM_CLAUDE_PROXY_ROOT") {
		t.Fatal("saved config lost its source reference or serialized runtime state")
	}
	var source Config
	if err := yaml.Unmarshal(before, &source); err != nil {
		t.Fatal(err)
	}
	// ProviderConfig uses mapstructure for legacy fields, so inspect only the
	// proxy's explicit YAML shape here; Load below checks full decoding.
	if p := source.Providers["native-alias"].Proxy; p == nil || p.Token != reference || p.URL != cfg.Providers["native-alias"].Proxy.URL {
		t.Fatal("provider-local proxy did not survive YAML round trip")
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pc := loaded.GetProviderConfig("native-alias")
	if pc == nil || pc.Proxy == nil || pc.Proxy.Token != reference || pc.ProxyBinding != "https://save-local.example.test/team/native" || pc.Model != "claude-sonnet-5-5" {
		t.Fatal("provider-local proxy did not survive save/load")
	}
	if b, ok := proxyclient.Lookup(pc.ProxyBinding); !ok || b.Token != token || b.Kind != "claude-code" {
		t.Fatal("file credential reference did not register the scoped binding")
	}
	if loaded.GetProviderConfig("claude-bin").Proxy != nil || loaded.GetProviderConfig("claude-bin").ProxyBinding != "" {
		t.Fatal("round trip opted regular Claude into proxy mode")
	}
	if err := Save(loaded); err == nil || !strings.Contains(err.Error(), "materialized proxy") {
		t.Fatal("materialized provider-local proxy must not be saved", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("refused save changed the source configuration", err)
	}
}

func TestProviderProxyLoadRejectsInvalidOptIn(t *testing.T) {
	for _, content := range []string{
		"providers:\n  claude-bin:\n    proxy: {}\n",
		"providers:\n  claude-bin:\n    proxy:\n      url: https://load-missing-token.example.test/route\n",
		"providers:\n  claude-bin:\n    proxy:\n      url: http://remote.example.test/route\n      token: scoped-token-long-enough\n",
		"providers:\n  claude-bin:\n    type: anthropic\n    proxy:\n      url: https://wrong-native-type.example.test/route\n      token: scoped-token-long-enough\n",
	} {
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", home)
		writeTestConfig(t, home, content)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "providers.claude-bin.proxy") {
			t.Fatalf("invalid native opt-in did not fail closed: %v", err)
		}
	}
}
