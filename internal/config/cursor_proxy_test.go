package config

import (
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestCursorProxyBlocksAPIKeyExchangeWithoutFallingBackToDirect(t *testing.T) {
	stubEmptyProxyDiscovery(t)
	for _, mode := range []string{"shared", "direct"} {
		t.Run(mode, func(t *testing.T) {
			const key = "synthetic-local-cursor-key"
			cfg := Config{
				Proxy: ProxyConfig{URL: "https://cursor-" + mode + ".example.test", Token: strings.Repeat("c", 40)},
				Providers: map[string]ProviderConfig{
					"cursor-bin": {Type: ProviderTypeCursorBin, Env: map[string]string{"CURSOR_API_KEY": key}},
				},
			}
			if mode == "direct" {
				cfg.Proxy.Direct = []string{"cursor-bin"}
			} else {
				cfg.Proxy.Providers = []string{"cursor-bin"}
			}
			if err := cfg.materializeProxy(nil); err != nil {
				t.Fatal(err)
			}
			pc := cfg.Providers["cursor-bin"]
			err := pc.ResolveForInference()
			if mode == "direct" {
				if err != nil || pc.Env["CURSOR_API_KEY"] != key || pc.ProxyBinding != "" {
					t.Fatalf("explicitly direct Cursor was changed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "/auth/exchange_user_api_key") ||
				pc.APIKey != "" || pc.ResolvedAPIKey != "" || len(pc.Env) != 0 {
				t.Fatalf("shared Cursor was not blocked with local credentials removed: %v", err)
			}
			for _, suffix := range []string{"", "-agent", "-repo"} {
				if _, ok := proxyclient.Lookup(cfg.Proxy.URL + "/cursor-bin" + suffix); ok {
					t.Errorf("unexpected Cursor binding for route cursor-bin%s", suffix)
				}
			}
		})
	}
}
