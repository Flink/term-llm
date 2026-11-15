package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBaseURL(t *testing.T) {
	t.Setenv("AUDIT_BASE", "https://proxy.example/nested/api/v1///")
	file := filepath.Join(t.TempDir(), "endpoint")
	if err := os.WriteFile(file, []byte("https://file.example/proxy/v1/"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ raw, fallback, want string }{
		{"", "https://default.example/v1/", "https://default.example/v1"},
		{"${AUDIT_BASE}", "https://default.example", "https://proxy.example/nested/api/v1"},
		{"file://" + file, "", "https://file.example/proxy/v1"},
		{"", "", ""},
	} {
		got, err := ResolveBaseURL("feature.base_url", tc.raw, tc.fallback)
		if err != nil || got != tc.want {
			t.Errorf("got=%q err=%v want=%q", got, err, tc.want)
		}
	}
	for _, raw := range []string{"ftp://example.test", "//example.test", "https://user:secret@example.test", "https://example.test/?token=secret", "https://example.test/#secret", "https://example.test/?", "https://example.test/#"} {
		_, err := ResolveBaseURL("feature.base_url", raw, "")
		if err == nil || !strings.Contains(err.Error(), "feature.base_url") || strings.Contains(err.Error(), "secret") {
			t.Errorf("error=%v", err)
		}
	}
}

func TestProviderBaseURLPrecedenceAndIsolation(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderConfig{"gemini": {BaseURL: "https://shared.example/prefix/", APIKey: "file:///not-used-by-endpoint", Headers: map[string]string{"unused": "file:///not-used"}}}}
	for _, tc := range []struct{ raw, want string }{{"https://feature.example/api/", "https://feature.example/api"}, {"", "https://shared.example/prefix"}} {
		got, err := cfg.ProviderBaseURL("gemini", tc.raw, "https://default.example")
		if err != nil || got != tc.want {
			t.Fatalf("got=%q err=%v", got, err)
		}
	}
	cfg.Providers["gemini"] = ProviderConfig{Type: ProviderTypeOpenAICompat, BaseURL: "https://different.example"}
	got, err := cfg.ProviderBaseURL("gemini", "", "https://default.example")
	if err != nil || got != "https://default.example" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	cfg.Providers["gemini"] = ProviderConfig{BaseURL: "file:///nonexistent-audit-base"}
	if _, err = cfg.ProviderBaseURL("gemini", "", "https://default.example"); err == nil {
		t.Fatal("explicit deferred failure fell back")
	}
}

func TestExplicitGrokSubscriptionBaseURL(t *testing.T) {
	if err := ValidateProviderCompatibility("grok", &ProviderConfig{Type: ProviderTypeGrok, BaseURL: "https://gateway.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBaseURLRejectsExplicitEmpty(t *testing.T) {
	t.Setenv("EMPTY_API_ROOT", "")
	emptyFile := filepath.Join(t.TempDir(), "empty-root")
	if err := os.WriteFile(emptyFile, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"${EMPTY_API_ROOT}", "file://" + emptyFile} {
		for _, fallback := range []string{"", "https://public.example/v1"} {
			got, err := ResolveBaseURL("feature.base_url", raw, fallback)
			if got != "" || err == nil || !strings.Contains(err.Error(), "feature.base_url resolved to an empty endpoint") {
				t.Errorf("raw=%q fallback=%q: got=%q err=%v", raw, fallback, got, err)
			}
		}
	}
	cfg := &Config{Providers: map[string]ProviderConfig{"gemini": {BaseURL: "https://shared.example/v1"}}}
	if _, err := cfg.ProviderBaseURL("gemini", "${EMPTY_API_ROOT}", "https://public.example/v1"); err == nil {
		t.Fatal("empty feature override fell through to shared root")
	}
	cfg.Providers["gemini"] = ProviderConfig{BaseURL: "${EMPTY_API_ROOT}"}
	if _, err := cfg.ProviderBaseURL("gemini", "", "https://public.example/v1"); err == nil {
		t.Fatal("empty shared override fell through to public root")
	}
}

func TestProviderResolutionRejectsExplicitEmptyBaseURL(t *testing.T) {
	t.Setenv("EMPTY_PROVIDER_ROOT", "")
	emptyFile := filepath.Join(t.TempDir(), "root")
	if err := os.WriteFile(emptyFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"${EMPTY_PROVIDER_ROOT}", "file://" + emptyFile} {
		t.Run(raw, func(t *testing.T) {
			cfg := &ProviderConfig{Type: ProviderTypeGemini, BaseURL: raw, URL: "$(printf https://gateway.example/chat)"}
			err := resolveProviderCredentials("gemini", cfg)
			if err == nil {
				err = cfg.ResolveForInference()
			}
			if err == nil || !strings.Contains(err.Error(), "empty endpoint") {
				t.Fatalf("provider resolution: base=%q err=%v", cfg.BaseURL, err)
			}
			if cfg.BaseURL != raw {
				t.Fatalf("failed resolution erased explicit root: got=%q want=%q", cfg.BaseURL, raw)
			}
			if cfg.needsLazyResolution {
				if err := cfg.ResolveForInference(); err == nil {
					t.Fatal("retry fell through to default root")
				}
			}
			c := &Config{Providers: map[string]ProviderConfig{"openai": {BaseURL: raw}}}
			if _, err := c.OpenAIEndpoint(); err == nil || !strings.Contains(err.Error(), "empty endpoint") {
				t.Fatalf("OpenAIEndpoint error=%v", err)
			}
		})
	}
}
