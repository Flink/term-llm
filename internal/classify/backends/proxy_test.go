package backends

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyprofile"

	"gopkg.in/yaml.v3"
)

const classifyProxyTestToken = "classify-client-test-token"

func loadClassifyProxy(t *testing.T, selection, providers, local string) (*config.Config, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/_proxy/client/v2/providers" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(proxyprofile.ClassificationDiscoveryHeader) != proxyprofile.ClassificationDiscoveryCapability+", "+proxyprofile.ClassificationDefaultCapability ||
			r.Header.Get("Authorization") != "Bearer "+classifyProxyTestToken {
			t.Error("classification discovery omitted capability or authentication")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": json.RawMessage(providers)})
	}))
	t.Cleanup(server.Close)
	t.Setenv("CLASSIFY_PROXY_TEST_TOKEN", classifyProxyTestToken)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := config.GetConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	yml := fmt.Sprintf("proxy:\n  url: %s\n  token: ${CLASSIFY_PROXY_TEST_TOKEN}\n%s%s", server.URL, selection, local)
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, server.URL
}

const classifyProxyProviders = `[
  {"id":"typesafe","family":"classification","routes":[{"name":"typesafe","profile":"typesafe","local_prefix":"/work/jev/"}]},
  {"id":"openai-decisions","family":"classification","routes":[{"name":"openai-decisions","profile":"openai-decisions","local_prefix":"/decisions/"}]},
  {"id":"cloudflare-clef","family":"classification","routes":[{"name":"cloudflare-clef","profile":"cloudflare-clef","local_prefix":"/clef/"}]}
]`

func TestResolveOptInClassifiersThroughProxy(t *testing.T) {
	// If any of these local references is resolved the backend must fail. Its
	// only source of credentials and headers should be the approved proxy route.
	missing := "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "missing"))
	local := `classify:
  default_provider: custom
  providers:
    custom:
      type: typesafe
      api_key: direct-test-key
      base_url: https://direct.example.test
    typesafe:
      model: jev-preview
      images: inline
      timeout_seconds: 21
      base_url: ` + missing + `
      api_key: ` + missing + `
    openai:
      api_key: ` + missing + `
      headers:
        OpenAI-Organization: ` + missing + "\n"
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("OPENAI_PROJECT_ID", "not-a-proxy-header")
	cfg, root := loadClassifyProxy(t, "  providers: [typesafe, openai-decisions, cloudflare-clef]\n", classifyProxyProviders, local)
	for _, tc := range []struct{ name, route string }{
		{"typesafe", "/work/jev"}, {"openai", "/decisions"}, {"cloudflare", "/clef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := cfg.Classify.ResolveProvider(tc.name)
			if err != nil || !p.CredentialsConfigured() {
				t.Fatalf("classifier was not available: %v", err)
			}
			conn, err := Resolve(p, Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if conn.BaseURL != root+tc.route || conn.APIKey != classifyProxyTestToken || len(conn.Headers) != 0 || conn.AccountID != "" {
				t.Fatal("classifier did not use the advertised endpoint and scoped credentials exclusively")
			}
			if tc.name == "typesafe" && (p.Model != "jev-preview" || conn.Timeout != 21*time.Second || conn.Images != "inline") {
				t.Fatal("local model, timeout or image mode changed")
			}
			if _, err := Resolve(p, Overrides{BaseURL: "https://not-the-proxy.example.test"}); err == nil || !strings.Contains(err.Error(), "cannot override") {
				t.Fatalf("managed override: %v", err)
			}
		})
	}
	alias, err := cfg.Classify.ResolveProvider("")
	if err != nil || cfg.Classify.DefaultProvider != "custom" {
		t.Fatalf("default selection changed: %v", err)
	}
	conn, err := Resolve(alias, Overrides{})
	if err != nil || conn.BaseURL != "https://direct.example.test" || conn.APIKey != "direct-test-key" {
		t.Fatal("unselected alias did not stay direct")
	}
	if err := config.Save(cfg); err == nil || !strings.Contains(err.Error(), "materialized") {
		t.Fatalf("saving materialized config: %v", err)
	}
	serialized, err := yaml.Marshal(cfg.Classify)
	if err != nil || strings.Contains(string(serialized), classifyProxyTestToken) {
		t.Fatal("scoped token entered serializable classifier configuration")
	}
}

func TestResolveMissingAndUnselectedClassifiers(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "direct-typesafe-key")
	// A pre-classification proxy advertises neither missing nor revoked routes.
	cfg, _ := loadClassifyProxy(t, "  providers: [typesafe]\n", "[]", "")
	p, err := cfg.Classify.ResolveProvider("typesafe")
	if err != nil || p.CredentialsConfigured() {
		t.Fatalf("ungranted classifier is usable: %v", err)
	}
	if _, err := Resolve(p, Overrides{}); err == nil || !strings.Contains(err.Error(), "not enabled and granted") {
		t.Fatalf("ungranted classifier resolution: %v", err)
	}
	for _, tc := range []struct{ selection, advertised string }{
		{"", "[]"},
		{"  direct: [typesafe, openai-decisions]\n", classifyProxyProviders},
		{"  providers: [openai]\n", classifyProxyProviders},
	} {
		cfg, _ := loadClassifyProxy(t, tc.selection, tc.advertised, "")
		p, err := cfg.Classify.ResolveProvider("typesafe")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := Resolve(p, Overrides{})
		if err != nil || conn.BaseURL != config.DefaultTypeSafeBaseURL || conn.APIKey != "direct-typesafe-key" {
			t.Fatalf("unselected classifier did not stay direct for selection %q: %v", tc.selection, err)
		}
		decisions, err := cfg.Classify.ResolveProvider("openai")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, managed, _ := decisions.ProxyConnection(); managed {
			t.Fatal("OpenAI chat selection also bound Decisions")
		}
	}
}

func TestNamedInferenceProviderCannotClaimClassifier(t *testing.T) {
	const providers = `[{"id":"typesafe","family":"inference","routes":[{"name":"typesafe","local_prefix":"/legacy-inference/"}]}]`
	cfg, _ := loadClassifyProxy(t, "  providers: [typesafe]\n", providers, "")
	p, err := cfg.Classify.ResolveProvider("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, managed, err := p.ProxyConnection(); !managed || err == nil {
		t.Fatal("explicit selection fell back despite no granted TypeSafe classification route")
	}
	if cfg.Providers["typesafe"].ProxyBinding == "" {
		t.Fatal("legacy custom inference binding was lost")
	}
}
