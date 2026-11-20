package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestProxyProvidersOnlyRoutesListedProviders(t *testing.T) {
	stubProxyDiscovery(t, func(_, _ string) (*proxyDiscoveryCatalog, error) {
		return validateDiscoveryPayload(proxyDiscoveryPayload{Providers: []proxyDiscoveredProvider{
			{ID: "work", Family: "inference", Routes: []proxyDiscoveredRoute{{Name: "work", LocalPrefix: "/work/", Profile: "chatgpt"}}},
		}})
	})
	tests := []struct {
		name, list     string
		want, notBound []string
	}{
		{"chatgpt-only", "chatgpt", []string{"chatgpt", "chatgpt-backend", "chatgpt-openai"}, []string{"venice", "work", "claude-bin", "gemini-live"}},
		{"chatgpt-and-claude", "chatgpt, claude-bin", []string{"chatgpt", "chatgpt-backend", "claude-bin"}, []string{"venice", "work", "anthropic"}},
		{"named-only", "work", []string{"work"}, []string{"chatgpt", "chatgpt-backend", "chatgpt-openai", "venice"}},
		{"standalone-only", "elevenlabs", []string{"elevenlabs"}, []string{"chatgpt", "chatgpt-backend", "venice", "work"}},
		{"none", "", nil, []string{"chatgpt", "chatgpt-backend", "elevenlabs", "venice", "work"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			root := "https://" + tt.name + ".proxy.example.test"
			writeTestConfig(t, home, `proxy:
  url: `+root+`
  token: test-client-token-0123456789
  providers: [`+tt.list+`]
providers:
  chatgpt:
    model: configured-model
  work:
    type: chatgpt
  venice:
    base_url: https://direct.example.test/api/v1
    api_key: local-venice-key
image:
  openai:
    base_url: https://local-openai.example.test/v1
`)
			cfg, err := LoadWithoutCredentials()
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range tt.want {
				endpoint := unavailableProxyEndpoint(root, id)
				if id == "work" {
					endpoint = root + "/work"
				}
				if b, ok := proxyclient.Lookup(endpoint); !ok || b.Root != endpoint {
					t.Errorf("expected binding for %s, got %+v", id, b)
				}
			}
			for _, id := range tt.notBound {
				endpoint := unavailableProxyEndpoint(root, id)
				if id == "work" {
					endpoint = root + "/work"
				}
				if b, ok := proxyclient.Lookup(endpoint); ok {
					t.Errorf("unselected integration %s registered binding %s", id, b.Root)
				}
			}
			if pc := cfg.Providers["venice"]; pc.ProxyBinding != "" || pc.BaseURL != "https://direct.example.test/api/v1" || pc.APIKey != "local-venice-key" {
				t.Errorf("Venice's direct configuration changed: %+v", pc)
			}
			if pc := cfg.Providers["chatgpt"]; pc.Model != "configured-model" {
				t.Errorf("ChatGPT model changed: %s", pc.Model)
			}
			if cfg.Image.OpenAI.BaseURL != "https://local-openai.example.test/v1" {
				t.Error("unrelated image endpoint was rewritten")
			}
			if strings.HasPrefix(tt.list, "chatgpt") {
				if cfg.Transcription.ChatGPT.BaseURL != unavailableProxyEndpoint(root, "chatgpt-backend") || cfg.Embed.ChatGPT.BaseURL != unavailableProxyEndpoint(root, "chatgpt-openai") {
					t.Error("ChatGPT feature endpoints did not follow the provider")
				}
			} else if strings.HasPrefix(cfg.Transcription.ChatGPT.BaseURL, root) || strings.HasPrefix(cfg.Embed.ChatGPT.BaseURL, root) {
				t.Error("built-in ChatGPT feature endpoints were unexpectedly proxied")
			}
			if tt.list == "chatgpt, claude-bin" {
				pc := cfg.Providers["claude-bin"]
				if pc.ProxyBinding != unavailableProxyEndpoint(root, "claude-bin") || pc.Env["TERM_LLM_CLAUDE_PROXY_ROOT"] != pc.ProxyBinding {
					t.Fatalf("Claude Code launcher wasn't configured: %+v", pc)
				}
			}
		})
	}
}

func TestProxyProvidersRejectsInvalidSelection(t *testing.T) {
	stubEmptyProxyDiscovery(t)
	for _, tc := range []struct{ yaml, want string }{
		{"providers: [a-typo]", "not a known provider"},
		{"providers: [claude-code]", "not a known provider"},
		{"providers: [chatgpt, chatgpt]", "duplicated"},
		{"providers: [chatgpt]\n  direct: [venice]", "cannot be combined with proxy.direct"},
		{"providers: [opencode-go-messages]", "select opencode-go as a whole"},
		{"providers: null", "must be a list"},
		{"providers: chatgpt", "proxy"},
		{"providers: [123]", "must contain only provider names"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			writeTestConfig(t, home, "proxy:\n  url: https://invalid-selection.proxy.example.test\n  token: test-client-token-0123456789\n  "+tc.yaml+"\n")
			if _, err := LoadWithoutCredentials(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestProxyProvidersSavePreservesSelection(t *testing.T) {
	for _, selection := range [][]string{nil, {}, {"chatgpt"}} {
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", home)
		if err := Save(&Config{Proxy: ProxyConfig{URL: "https://proxy-save-selection.example.test", Token: "file:///proxy-token", Providers: selection}}); err != nil {
			t.Fatal(err)
		}
		file, err := readFileSettings(filepath.Join(home, "term-llm", "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var p ProxyConfig
		if err := p.readProviderSelection(file); err != nil || !reflect.DeepEqual(p.Providers, selection) {
			t.Fatalf("saved list = %#v, want %#v; err = %v", p.Providers, selection, err)
		}
		if token, _ := file.lookup("proxy.token"); token != "file:///proxy-token" {
			t.Fatalf("credential reference was not preserved: %v", token)
		}
	}
}

func TestProxyProvidersRefusesToSaveMaterializedFeatureCredentials(t *testing.T) {
	stubEmptyProxyDiscovery(t)
	cfg := Config{Proxy: ProxyConfig{URL: "https://proxy-save-feature.example.test", Token: strings.Repeat("s", 40), Providers: []string{"elevenlabs"}}}
	if err := cfg.materializeProxy(nil); err != nil {
		t.Fatal(err)
	}
	if err := Save(&cfg); err == nil || !strings.Contains(err.Error(), "materialized proxy") {
		t.Fatalf("should refuse to save rewritten feature endpoints, got %v", err)
	}
}
