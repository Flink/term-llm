package usage

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const usageProxyTestToken = "sk-tlp-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const usageProxyTestBody = `{"five_hour":{"utilization":42.4,"resets_at":"2026-11-17T13:30:00Z"},"seven_day":{"utilization":12,"resets_at":null},"seven_day_oauth_apps":{"utilization":3},"seven_day_opus":{"utilization":4},"seven_day_sonnet":{"utilization":5},"model_scoped":[{"display_name":"Other","utilization":6}],"extra_usage":{"is_enabled":true,"monthly_limit":100,"used_credits":12,"utilization":12,"currency":"USD"}}`

const fakeClaudeUsageScript = `#!/bin/sh
{
  printf 'CLAUDE_CODE_OAUTH_TOKEN=%s\n' "$CLAUDE_CODE_OAUTH_TOKEN"
  printf 'CLAUDE_CODE_OAUTH_SCOPES=%s\n' "$CLAUDE_CODE_OAUTH_SCOPES"
  printf 'HTTPS_PROXY=%s\n' "$HTTPS_PROXY"
  printf 'HTTP_PROXY=%s\n' "$HTTP_PROXY"
  printf 'NO_PROXY=%s\n' "$NO_PROXY"
  printf 'no_proxy=%s\n' "$no_proxy"
  printf 'ALL_PROXY=%s\n' "$ALL_PROXY"
  printf 'NODE_EXTRA_CA_CERTS=%s\n' "$NODE_EXTRA_CA_CERTS"
  printf 'CLAUDE_CONFIG_DIR=%s\n' "$CLAUDE_CONFIG_DIR"
  printf 'ANTHROPIC_CONFIG_DIR=%s\n' "$ANTHROPIC_CONFIG_DIR"
  printf 'ANTHROPIC_BASE_URL=%s\n' "$ANTHROPIC_BASE_URL"
  printf 'ANTHROPIC_AUTH_TOKEN=%s\n' "$ANTHROPIC_AUTH_TOKEN"
  printf 'ANTHROPIC_API_KEY=%s\n' "$ANTHROPIC_API_KEY"
  printf 'CLAUDE_CODE_API_BASE_URL=%s\n' "$CLAUDE_CODE_API_BASE_URL"
  printf 'ANTHROPIC_UNIX_SOCKET=%s\n' "$ANTHROPIC_UNIX_SOCKET"
  printf 'https_proxy=%s\n' "$https_proxy"
  printf 'http_proxy=%s\n' "$http_proxy"
} > "$FAKE_USAGE_ENV_FILE"
read -r request
case "$request" in *get_usage*) ;; *) exit 93 ;; esac
printf '%s\n' "$FAKE_USAGE_RESPONSE"
`

func usageProxyTestCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatal(err)
	}
	return network
}

func usageProxyTestCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "term-llm usage proxy test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"api.anthropic.com", "platform.claude.com"},
		ExcludedDNSDomains:          []string{".api.anthropic.com", ".platform.claude.com"},
		ExcludedIPRanges:            []*net.IPNet{usageProxyTestCIDR(t, "0.0.0.0/0"), usageProxyTestCIDR(t, "::/0")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func isolateClaudeUsage(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cache := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", cache)
	for _, key := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_UNIX_SOCKET", "ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_SERVICE_ACCOUNT_ID", "ANTHROPIC_WORKSPACE_ID",
		"ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_API_KEY", "AWS_BEARER_TOKEN_BEDROCK",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"CLAUDE_CODE_API_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES", "CLAUDE_CODE_OAUTH_CLIENT_ID", "CLAUDE_CODE_CUSTOM_OAUTH_URL",
		"CLAUDE_CODE_SESSIONS_OAUTH_TOKEN", "CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_API_KEY_HELPER_FD",
		"CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_HOST_AUTH_ENV_VAR", "CCR_OAUTH_TOKEN_FILE",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CONFIG_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "NODE_TLS_REJECT_UNAUTHORIZED",
		"FAKE_USAGE_ENV_FILE", "FAKE_USAGE_RESPONSE", "FAKE_USAGE_MARKER",
	} {
		t.Setenv(key, "")
	}
	old := loadProviderUsageConfig
	t.Cleanup(func() { loadProviderUsageConfig = old })
	loadProviderUsageConfig = func() (*config.Config, error) { return &config.Config{}, nil }
	return home
}

func bindClaudeUsageTestProxy(t *testing.T, root string) {
	t.Helper()
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: "claude-code", Token: usageProxyTestToken}}); err != nil {
		t.Fatal(err)
	}
	loadProviderUsageConfig = func() (*config.Config, error) {
		return &config.Config{Providers: map[string]config.ProviderConfig{"claude-bin": {ProxyBinding: root}}}, nil
	}
}

func installFakeClaude(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell CLI fixture")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// Keep shell utilities available to the fixture while the fake CLI stays
	// first in PATH; no test reaches a real Claude installation.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func usageProxyMetadataServer(t *testing.T, caPEM []byte, route string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/.well-known/term-llm/claude-proxy") || r.URL.RawQuery != "" {
			t.Errorf("unexpected Claude metadata request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+usageProxyTestToken || r.Header.Get("x-api-key") != "" {
			t.Errorf("unexpected Claude metadata credential")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 2, "token_mode": "vault",
			"hosts":           []string{"api.anthropic.com", "platform.claude.com"},
			"certificate_pem": string(caPEM), "proxy_url": "http://" + r.Host, "route": route,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func produceFakeUsageResponse(t *testing.T) {
	t.Helper()
	t.Setenv("FAKE_USAGE_ENV_FILE", filepath.Join(t.TempDir(), "env"))
	t.Setenv("FAKE_USAGE_RESPONSE", `{"type":"control_response","response":{"subtype":"success","request_id":"term-llm-usage","response":{"rate_limits_available":true,"rate_limits":`+usageProxyTestBody+`}}}`)
	installFakeClaude(t, fakeClaudeUsageScript)
}

func fakeUsageEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	return values
}

func TestClaudeBinProxyUsageUsesScopedNativeGetUsage(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			isolateClaudeUsage(t)
			produceFakeUsageResponse(t)
			caPEM := usageProxyTestCA(t)
			route := "claude-code"
			server := usageProxyMetadataServer(t, caPEM, route)
			root := server.URL + "/mounted/claude-code"
			bindClaudeUsageTestProxy(t, root)
			// A configured binding must not read a user's local settings or
			// vendor credentials.
			t.Setenv("CLAUDE_CONFIG_DIR", "/must-not-read")
			t.Setenv("ANTHROPIC_API_KEY", "secret-local-vendor-key")
			t.Setenv("ANTHROPIC_UNIX_SOCKET", "/secret/local.sock")
			var out bytes.Buffer
			if err := runClaudeBinUsage(context.Background(), &out, jsonOutput); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), usageProxyTestToken) || strings.Contains(out.String(), "secret-local") {
				t.Fatalf("output leaked a credential: %s", &out)
			}
			values := fakeUsageEnv(t, os.Getenv("FAKE_USAGE_ENV_FILE"))
			if values["CLAUDE_CODE_OAUTH_TOKEN"] != "" || values["CLAUDE_CODE_OAUTH_SCOPES"] != "" {
				t.Fatal("launcher injected a vendor OAuth identity")
			}
			if values["NO_PROXY"] != values["no_proxy"] || !strings.Contains(values["NO_PROXY"], "localhost") || !strings.Contains(values["NO_PROXY"], "127.0.0.1") || !strings.Contains(values["NO_PROXY"], "::1") {
				t.Fatal("native API exclusion list does not keep loopback outside the tunnel")
			}
			if strings.Contains(strings.ToLower(values["NO_PROXY"]), "api.anthropic.com") || strings.Contains(strings.ToLower(values["NO_PROXY"]), "platform.claude.com") || strings.Contains(values["NO_PROXY"], "*") {
				t.Fatal("native Anthropic host could bypass the tunnel")
			}
			for _, key := range []string{"ALL_PROXY", "https_proxy", "http_proxy"} {
				if values[key] != "" {
					t.Fatalf("ambient proxy %s reached the native CLI", key)
				}
			}
			if values["HTTPS_PROXY"] != values["HTTP_PROXY"] || values["HTTPS_PROXY"] == "" {
				t.Fatal("native proxy variables differ")
			}
			proxy, err := url.Parse(values["HTTPS_PROXY"])
			if err != nil {
				t.Fatal(err)
			}
			password, _ := proxy.User.Password()
			if proxy.User.Username() != route || password != usageProxyTestToken || proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
				t.Fatal("native proxy URL is not the scoped origin")
			}
			for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_API_BASE_URL", "ANTHROPIC_UNIX_SOCKET"} {
				if values[key] != "" {
					t.Fatalf("vendor credential %s reached the native CLI", key)
				}
			}
			expectedState, err := proxyclient.ClaudeNativeStateDir(root, usageProxyTestToken)
			if err != nil {
				t.Fatal(err)
			}
			if values["CLAUDE_CONFIG_DIR"] != expectedState || values["ANTHROPIC_CONFIG_DIR"] != expectedState {
				t.Fatalf("native config dirs = %q/%q, want private isolated state", values["CLAUDE_CONFIG_DIR"], values["ANTHROPIC_CONFIG_DIR"])
			}
			caPath := values["NODE_EXTRA_CA_CERTS"]
			if filepath.Dir(caPath) != expectedState {
				t.Fatalf("CA is not in isolated native state: %q", caPath)
			}
			if info, err := os.Stat(caPath); err != nil {
				t.Fatalf("CA stat: %v", err)
			} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("CA mode = %v", info.Mode().Perm())
			}
			if _, err := os.Stat(filepath.Join(expectedState, ".claude.json")); !os.IsNotExist(err) {
				t.Fatal("proxy usage manufactured onboarding completion state")
			}
			if _, err := os.Stat(filepath.Join(expectedState, ".credentials.json")); !os.IsNotExist(err) {
				t.Fatal("proxy usage created a vendor credential file")
			}
			if jsonOutput {
				var report claudeBinUsageReport
				if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.RateLimitsAvailable || report.RateLimits == nil || report.SubscriptionType != nil {
					t.Fatalf("unexpected JSON envelope: %s (%v)", &out, err)
				}
				if got := *report.RateLimits.FiveHour.Utilization; got != 42.4 || len(report.RateLimits.ModelScoped) != 1 || report.RateLimits.ExtraUsage == nil || report.RateLimits.SevenDayOAuthApps == nil {
					t.Fatalf("raw Anthropic fields lost: %s", &out)
				}
			} else if !strings.Contains(out.String(), "Live through credential proxy") || strings.Contains(out.String(), "Live from Claude Code") {
				t.Fatalf("wrong usage source: %s", &out)
			}
		})
	}
}

func TestClaudeBinProxyUsageRejectsBootstrapErrorsWithoutFallback(t *testing.T) {
	base := `{"protocol_version":2,"token_mode":"vault","hosts":["api.anthropic.com","platform.claude.com"],"certificate_pem":"secret-body","proxy_url":"$PROXY","route":"claude-code"}`
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{name: "client unauthorized", status: 401, body: "secret-body", want: "scoped client token"},
		{name: "route forbidden", status: 403, body: "secret-body", want: "scoped client token"},
		{name: "gateway unavailable", status: 502, body: "secret-body", want: "HTTP 502"},
		{name: "malformed metadata", status: 200, body: `{"certificate_pem":`, want: "invalid"},
		{name: "wrong proxy origin", status: 200, body: strings.Replace(base, `"$PROXY"`, `"http://user:secret-body@elsewhere.test"`, 1), want: "proxy_url"},
		{name: "bad route", status: 200, body: strings.Replace(base, `"claude-code"`, `"../secret"`, 1), want: "route slug"},
		{name: "bad certificate", status: 200, body: base, want: "certificate_pem"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateClaudeUsage(t)
			marker := filepath.Join(t.TempDir(), "native-executed")
			t.Setenv("FAKE_USAGE_MARKER", marker)
			installFakeClaude(t, "#!/bin/sh\nprintf ran > \"$FAKE_USAGE_MARKER\"\nexit 99\n")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, strings.ReplaceAll(tt.body, "$PROXY", "http://"+r.Host))
			}))
			defer server.Close()
			bindClaudeUsageTestProxy(t, server.URL+"/claude-code")
			var out bytes.Buffer
			err := runClaudeBinUsage(context.Background(), &out, true)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			for _, secret := range []string{usageProxyTestToken, "secret-body", "/claude-code"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("bootstrap failure fell back to the local Claude CLI")
			}
			if out.Len() != 0 {
				t.Fatalf("bootstrap failure wrote output: %s", &out)
			}
		})
	}
}

func TestClaudeBinProxyUsageRejectsRedirect(t *testing.T) {
	isolateClaudeUsage(t)
	marker := filepath.Join(t.TempDir(), "native-executed")
	t.Setenv("FAKE_USAGE_MARKER", marker)
	installFakeClaude(t, "#!/bin/sh\nprintf ran > \"$FAKE_USAGE_MARKER\"\nexit 99\n")
	escape := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("proxy metadata redirect was followed")
	}))
	defer escape.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, escape.URL+"/secret-body", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	bindClaudeUsageTestProxy(t, origin.URL+"/claude-code")
	err := runClaudeBinUsage(context.Background(), io.Discard, true)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("redirect was not refused: %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("redirect fell back to the local Claude CLI")
	}
}

func TestClaudeBinProxyUsageSanitizesNativeErrors(t *testing.T) {
	for _, tt := range []struct {
		name, script string
	}{
		{name: "stderr and stdout", script: `#!/bin/sh
printf 'secret stdout %s\n' "$CLAUDE_CODE_OAUTH_TOKEN"
printf 'secret stderr %s\n' "$HTTPS_PROXY" >&2
exit 7
`},
		{name: "control error payload", script: `#!/bin/sh
read -r request
printf '%s\n' '{"type":"control_response","response":{"subtype":"error","request_id":"term-llm-usage","error":"secret token-secret central failure"}}'
`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateClaudeUsage(t)
			installFakeClaude(t, tt.script)
			caPEM := usageProxyTestCA(t)
			server := usageProxyMetadataServer(t, caPEM, "claude-code")
			bindClaudeUsageTestProxy(t, server.URL+"/claude-code")
			err := runClaudeBinUsage(context.Background(), io.Discard, true)
			if err == nil || !strings.Contains(err.Error(), "failed through proxy") {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, secret := range []string{usageProxyTestToken, "secret", "HTTP_PROXY", "token-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("proxied error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestClaudeBinProxyUsageDeadlinePreserved(t *testing.T) {
	isolateClaudeUsage(t)
	installFakeClaude(t, "#!/bin/sh\nwhile :; do :; done\n")
	caPEM := usageProxyTestCA(t)
	server := usageProxyMetadataServer(t, caPEM, "claude-code")
	bindClaudeUsageTestProxy(t, server.URL+"/claude-code")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := runClaudeBinUsage(ctx, io.Discard, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not propagated: %v", err)
	}
	if strings.Contains(err.Error(), usageProxyTestToken) {
		t.Fatalf("deadline error leaked token: %v", err)
	}
}

func TestClaudeBinProxyUsageInvalidBindingNoFallback(t *testing.T) {
	for _, kind := range []string{"unregistered", "chatgpt", "vendor-token", "child-path", "load-error"} {
		t.Run(kind, func(t *testing.T) {
			isolateClaudeUsage(t)
			marker := filepath.Join(t.TempDir(), "native-executed")
			t.Setenv("FAKE_USAGE_MARKER", marker)
			installFakeClaude(t, "#!/bin/sh\nprintf ran > \"$FAKE_USAGE_MARKER\"\nexit 99\n")
			root := "https://invalid-usage-binding.example.test/" + kind
			if kind == "chatgpt" || kind == "vendor-token" || kind == "child-path" {
				binding := proxyclient.Binding{Root: root, Kind: "claude-code", Token: usageProxyTestToken}
				if kind == "chatgpt" {
					binding.Kind = "chatgpt"
				}
				if kind == "vendor-token" {
					binding.Token = "sk-ant-secret-local-vendor-token"
				}
				if err := proxyclient.Register([]proxyclient.Binding{binding}); err != nil {
					t.Fatal(err)
				}
				if kind == "child-path" {
					root += "/unregistered-operation"
				}
			}
			loadProviderUsageConfig = func() (*config.Config, error) {
				if kind == "load-error" {
					return nil, errors.New("secret-config-body")
				}
				return &config.Config{Providers: map[string]config.ProviderConfig{"claude-bin": {ProxyBinding: root, APIKey: "secret-local-vendor-key"}}}, nil
			}
			var out bytes.Buffer
			err := runClaudeBinUsage(context.Background(), &out, true)
			if err == nil || strings.Contains(err.Error(), "CLI") || strings.Contains(err.Error(), "secret-") || out.Len() != 0 {
				t.Fatalf("err=%v output=%s", err, &out)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("invalid binding fell back to the local Claude CLI")
			}
		})
	}
}

func TestClaudeBinUsageDirectNativeUnaffected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell CLI fixture")
	}
	home := isolateClaudeUsage(t)
	marker := filepath.Join(t.TempDir(), "native-executed")
	t.Setenv("FAKE_USAGE_MARKER", marker)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-native-cli")
	cli := `#!/bin/sh
printf ran > "$FAKE_USAGE_MARKER"
[ -z "$ANTHROPIC_API_KEY" ] || exit 91
[ "$1" = "-p" ] || exit 92
read -r request
case "$request" in *get_usage*) ;; *) exit 93 ;; esac
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"term-llm-usage","response":{"subscription_type":"max","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":25}}}}}'
`
	installFakeClaude(t, cli)
	// With no owned receipt, this file must not change the established native path.
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"apiKeyHelper":"never execute here"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runClaudeBinUsage(context.Background(), &out, false); err != nil || !strings.Contains(out.String(), "Live from Claude Code") || !strings.Contains(out.String(), "25% used") {
		t.Fatalf("native output=%s err=%v", &out, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("direct native path did not execute the CLI")
	}
}

func TestClaudeBinDirectNativeErrorsRemainDetailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell CLI fixture")
	}
	isolateClaudeUsage(t)
	installFakeClaude(t, "#!/bin/sh\nprintf 'direct-native-detail\n' >&2\nexit 7\n")
	err := runClaudeBinUsage(context.Background(), io.Discard, true)
	if err == nil || !strings.Contains(err.Error(), "direct-native-detail") {
		t.Fatalf("direct native error lost detail: %v", err)
	}
}

func TestClaudeBinProxyUsageNullWindows(t *testing.T) {
	isolateClaudeUsage(t)
	t.Setenv("FAKE_USAGE_ENV_FILE", filepath.Join(t.TempDir(), "env"))
	t.Setenv("FAKE_USAGE_RESPONSE", `{"type":"control_response","response":{"subtype":"success","request_id":"term-llm-usage","response":{"rate_limits_available":true,"rate_limits":{"five_hour":null,"seven_day":null}}}}`)
	installFakeClaude(t, fakeClaudeUsageScript)
	server := usageProxyMetadataServer(t, usageProxyTestCA(t), "claude-code")
	bindClaudeUsageTestProxy(t, server.URL+"/claude-code")
	var out bytes.Buffer
	if err := runClaudeBinUsage(context.Background(), &out, false); err != nil || !strings.Contains(out.String(), "No active usage windows were returned.") || !strings.Contains(out.String(), "Live through credential proxy") || strings.Contains(out.String(), "user:profile") {
		t.Fatalf("null-window output=%s err=%v", &out, err)
	}
	out.Reset()
	if err := runClaudeBinUsage(context.Background(), &out, true); err != nil {
		t.Fatal(err)
	}
	var report claudeBinUsageReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.RateLimitsAvailable || report.RateLimits == nil || report.RateLimits.FiveHour != nil || report.RateLimits.SevenDay != nil {
		t.Fatalf("null-window JSON=%s err=%v", &out, err)
	}
}

type claudeUsageCustomTransport struct{}

func (claudeUsageCustomTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("custom global transport must not be invoked")
}

func TestClaudeBinProxyUsageCustomDefaultTransport(t *testing.T) {
	isolateClaudeUsage(t)
	produceFakeUsageResponse(t)
	old := http.DefaultTransport
	http.DefaultTransport = claudeUsageCustomTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	server := usageProxyMetadataServer(t, usageProxyTestCA(t), "claude-code")
	bindClaudeUsageTestProxy(t, server.URL+"/claude-code")
	if err := runClaudeBinUsage(context.Background(), io.Discard, true); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeBinProxyUsageDoesNotConsultInstalledVendorCredentials(t *testing.T) {
	home := isolateClaudeUsage(t)
	marker := filepath.Join(t.TempDir(), "native-executed")
	t.Setenv("FAKE_USAGE_MARKER", marker)
	installFakeClaude(t, "#!/bin/sh\nprintf ran > \"$FAKE_USAGE_MARKER\"\nexit 99\n")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"accessToken":"secret-local-vendor-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loadProviderUsageConfig = func() (*config.Config, error) {
		return &config.Config{Providers: map[string]config.ProviderConfig{"claude-bin": {
			ProxyBinding: "https://missing-usage-binding.example.test/claude-code",
			APIKey:       "secret-local-vendor-key",
		}}}, nil
	}
	err := runClaudeBinUsage(context.Background(), io.Discard, true)
	if err == nil || strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), "CLI") {
		t.Fatalf("installed vendor credential was consulted: %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("configured proxy binding fell back to the local Claude CLI")
	}
}
