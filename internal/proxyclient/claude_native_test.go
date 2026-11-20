package proxyclient

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
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func claudeNativeTestCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatal(err)
	}
	return network
}

func claudeNativeTestCA(t *testing.T, mutate func(*x509.Certificate)) []byte {
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
	cert := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "term-llm test Claude proxy"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{claudeNativeAPIHost, claudeNativePlatformHost},
		ExcludedDNSDomains:          []string{"." + claudeNativeAPIHost, "." + claudeNativePlatformHost},
		ExcludedIPRanges:            []*net.IPNet{claudeNativeTestCIDR(t, "0.0.0.0/0"), claudeNativeTestCIDR(t, "::/0")},
	}
	if mutate != nil {
		mutate(cert)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

const claudeNativeTestToken = "sk-tlp-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// claudeNativeTestMetadata returns a complete v2 bootstrap document. ProxyURL
// is supplied by each test because httptest binds the host at construction.
func claudeNativeTestMetadata(caPEM []byte, proxyURL, route string) map[string]any {
	return map[string]any{
		"protocol_version": nativeProtocolVersion,
		"token_mode":       nativeTokenMode,
		"hosts":            claudeNativeHosts,
		"certificate_pem":  string(caPEM),
		"proxy_url":        proxyURL,
		"route":            route,
	}
}

func envEntryMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func TestClaudeNativeNoProxyKeepsLoopbackAndSafeEntries(t *testing.T) {
	for _, entry := range []string{"api.anthropic.com", "API.ANTHROPIC.COM", ".api.anthropic.com", "api.anthropic.com:443", "api.anthropic.com/path", "api.anthropic.com::", "..com..", "example.org api.anthropic.com", "http://api.anthropic.com", "0.0.0.0/0", "::/0", "anthropic.com", ".anthropic.com", "com", ".com", "foo.api.anthropic.com", "*", "*.anthropic.com", "api.anthropic.com.", ":443", "example.org\tapi.anthropic.com", "ｅxample.org", "platform.claude.com", "PLATFORM.CLAUDE.COM", ".platform.claude.com", "platform.claude.com:443", "claude.com", ".claude.com", "foo.platform.claude.com", "*.claude.com", "platform.claude.com."} {
		if !claudeNativeNoProxyCouldReachAPI(entry) {
			t.Errorf("unsafe no-proxy entry allowed: %q", entry)
		}
	}
	for _, entry := range []string{"localhost", "127.0.0.1", "::1", "[::1]", "[::1]:443", "corp.internal", ".example.test", "localhost:3000", "...localhost..."} {
		if claudeNativeNoProxyCouldReachAPI(entry) {
			t.Errorf("safe no-proxy entry rejected: %q", entry)
		}
	}
	got := ClaudeNativeNoProxy([]string{
		"NO_PROXY=localhost,.example.test,api.anthropic.com,platform.claude.com,*.evil.test,anthropic.com,.claude.com,.com",
		"no_proxy=127.0.0.1,::1,corp.internal,foo.api.anthropic.com,foo.platform.claude.com",
	})
	entries := strings.Split(got, ",")
	for _, want := range []string{"localhost", "127.0.0.1", "::1", ".example.test", "corp.internal"} {
		if !slices.Contains(entries, want) {
			t.Errorf("safe entry %q missing from %q", want, got)
		}
	}
	for _, entry := range entries {
		if strings.Contains(entry, "*") || strings.EqualFold(entry, "api.anthropic.com") || strings.EqualFold(entry, "platform.claude.com") || strings.HasSuffix(strings.ToLower(entry), "anthropic.com") || strings.HasSuffix(strings.ToLower(entry), "claude.com") || strings.HasSuffix(strings.ToLower(entry), ".com") {
			t.Fatalf("Anthropic host bypass entry retained: %q", got)
		}
	}
	if again := ClaudeNativeNoProxy([]string{"NO_PROXY=" + got}); again != got {
		t.Fatalf("canonical no-proxy value is not idempotent: %q != %q", again, got)
	}
}

func TestPrepareClaudeNativeEnvironmentUsesScopedTunnelAndPrivateState(t *testing.T) {
	caPEM := claudeNativeTestCA(t, nil)
	proxyOrigin := ""
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeNativeTestMetadata(caPEM, proxyOrigin, "claude-bin"))
	}))
	defer metadata.Close()
	proxyOrigin = metadata.URL
	root := proxyOrigin + "/team/claude-bin"
	stateDir := filepath.Join(t.TempDir(), "nested", "claude-state")
	env := []string{"ANTHROPIC_BASE_URL=https://vendor.example.test",
		"ANTHROPIC_AUTH_TOKEN=vendor-secret",
		"ANTHROPIC_API_KEY=vendor-secret",
		"CLAUDE_CODE_API_BASE_URL=https://shim.example.test",
		"CLAUDE_CODE_OAUTH_TOKEN=vendor-oauth",
		"CLAUDE_CODE_OAUTH_TOKEN_FILE=/vendor/token",
		"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR=8",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN=vendor-refresh",
		"CLAUDE_CODE_OAUTH_SCOPES=user:inference",
		"CLAUDE_CODE_OAUTH_CLIENT_ID=vendor-client",
		"CLAUDE_CODE_CUSTOM_OAUTH_URL=https://oauth.example.test",
		"CLAUDE_CODE_SESSIONS_OAUTH_TOKEN=vendor-session-oauth",
		"CLAUDE_CODE_SESSION_ACCESS_TOKEN=vendor-session",
		"CLAUDE_CODE_API_KEY_HELPER_FD=7",
		"CLAUDE_CODE_HOST_CREDS_FILE=/vendor/host-creds",
		"CLAUDE_CODE_HOST_AUTH_ENV_VAR=VENDOR_SECRET",
		"CCR_OAUTH_TOKEN_FILE=/vendor/ccr-token",
		"ANTHROPIC_PROFILE=vendor-profile",
		"ANTHROPIC_CONFIG_DIR=/ambient/anthropic",
		"ANTHROPIC_FEDERATION_RULE_ID=fdrl_vendor",
		"ANTHROPIC_ORGANIZATION_ID=vendor-org",
		"ANTHROPIC_IDENTITY_TOKEN=vendor-jwt",
		"ANTHROPIC_IDENTITY_TOKEN_FILE=/vendor/jwt",
		"ANTHROPIC_SERVICE_ACCOUNT_ID=svac_vendor",
		"ANTHROPIC_WORKSPACE_ID=wrkspc_vendor",
		"ANTHROPIC_GOOGLE_CLOUD_PROJECT=vendor-project",
		"ANTHROPIC_AWS_API_KEY=vendor-aws-key",
		"ANTHROPIC_UNIX_SOCKET=/vendor/anthropic.sock",
		"ANTHROPIC_CUSTOM_HEADERS=x-api-key: vendor-secret",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_USE_VERTEX=1",
		"CLAUDE_CODE_USE_FOUNDRY=1",
		"CLAUDE_CODE_USE_ANTHROPIC_AWS=1",
		"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD=1",
		"CLAUDE_CODE_USE_MANTLE=1",
		"ANTHROPIC_BEDROCK_BASE_URL=https://bedrock.example.test",
		"ANTHROPIC_VERTEX_BASE_URL=https://vertex.example.test",
		"ANTHROPIC_FOUNDRY_BASE_URL=https://foundry.example.test",
		"ANTHROPIC_FOUNDRY_API_KEY=vendor-secret",
		"AWS_BEARER_TOKEN_BEDROCK=vendor-secret",
		"AWS_ACCESS_KEY_ID=vendor-secret",
		"AWS_SECRET_ACCESS_KEY=vendor-secret",
		"AWS_SESSION_TOKEN=vendor-secret",
		"HTTP_PROXY=http://ambient.example.test:8080",
		"HTTPS_PROXY=http://ambient.example.test:8080",
		"ALL_PROXY=socks5://ambient.example.test:1080",
		"NO_PROXY=api.anthropic.com,platform.claude.com,.claude.com,.example.test,corp.internal,*.evil.test,anthropic.com,.com",
		"http_proxy=http://ambient.example.test:8080",
		"https_proxy=http://ambient.example.test:8080",
		"all_proxy=socks5://ambient.example.test:1080",
		"no_proxy=api.anthropic.com,platform.claude.com,127.0.0.1,::1,corp.internal",
		"NODE_EXTRA_CA_CERTS=/ambient/ca.pem",
		"NODE_TLS_REJECT_UNAUTHORIZED=0",
		"CLAUDE_CONFIG_DIR=/ambient/claude",
		"TERM_LLM_CLAUDE_PROXY_ROOT=https://ambient.example.test/claude-bin",
		"CLAUDE_CODE_DISABLE_WORKFLOWS=1",
		"ENABLE_TOOL_SEARCH=false",
		"PRESERVED=value",
	}
	got, err := PrepareClaudeNativeEnvironment(context.Background(), root, claudeNativeTestToken, stateDir, env)
	if err != nil {
		t.Fatal(err)
	}
	values := envEntryMap(got)
	if _, ok := values["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Fatal("launcher must not inject a vendor OAuth token")
	}
	if _, ok := values["CLAUDE_CODE_OAUTH_SCOPES"]; ok {
		t.Fatal("launcher must not inject OAuth scopes")
	}
	if values["ANTHROPIC_CUSTOM_HEADERS"] != "X-Term-LLM-Proxy-App: term-llm" {
		t.Fatal("missing bounded application attribution")
	}
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_PROFILE", "ANTHROPIC_UNIX_SOCKET", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_SERVICE_ACCOUNT_ID", "ANTHROPIC_WORKSPACE_ID", "ANTHROPIC_GOOGLE_CLOUD_PROJECT", "ANTHROPIC_AWS_API_KEY", "CLAUDE_CODE_API_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN_FILE", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_CLIENT_ID", "CLAUDE_CODE_CUSTOM_OAUTH_URL", "CLAUDE_CODE_SESSIONS_OAUTH_TOKEN", "CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_API_KEY_HELPER_FD", "CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_HOST_AUTH_ENV_VAR", "CCR_OAUTH_TOKEN_FILE", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NODE_TLS_REJECT_UNAUTHORIZED", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		if _, ok := values[key]; ok {
			t.Errorf("competing environment entry %s was preserved", key)
		}
	}
	for _, key := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_API_KEY", "AWS_BEARER_TOKEN_BEDROCK", "TERM_LLM_CLAUDE_PROXY_ROOT"} {
		if _, ok := values[key]; ok {
			t.Errorf("cloud credential entry %s was preserved", key)
		}
	}
	if values["NO_PROXY"] != values["no_proxy"] || values["NO_PROXY"] == "" {
		t.Fatal("native exclusion lists differ or are missing")
	}
	noProxy := strings.Split(values["NO_PROXY"], ",")
	for _, want := range []string{"localhost", "127.0.0.1", "::1", ".example.test", "corp.internal"} {
		if !slices.Contains(noProxy, want) {
			t.Errorf("safe no-proxy entry %q was not preserved: %q", want, values["NO_PROXY"])
		}
	}
	for _, entry := range noProxy {
		lower := strings.ToLower(entry)
		if lower == "api.anthropic.com" || lower == "platform.claude.com" || strings.Contains(lower, "*") || strings.HasSuffix(lower, "anthropic.com") || strings.HasSuffix(lower, "claude.com") || strings.HasSuffix(lower, ".com") {
			t.Fatalf("Anthropic host could bypass the native tunnel: %q", values["NO_PROXY"])
		}
	}
	if values["PRESERVED"] != "value" || values["CLAUDE_CODE_DISABLE_WORKFLOWS"] != "1" || values["ENABLE_TOOL_SEARCH"] != "false" {
		t.Fatal("unrelated or isolation environment was not preserved")
	}
	if values["HTTPS_PROXY"] != values["HTTP_PROXY"] || values["HTTPS_PROXY"] == "" {
		t.Fatal("native proxy variables differ or are missing")
	}
	proxy, err := url.Parse(values["HTTPS_PROXY"])
	if err != nil || proxy.Scheme != "http" || proxy.Host != strings.TrimPrefix(proxyOrigin, "http://") || proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
		t.Fatalf("unexpected authenticated proxy URL: %v", err)
	}
	password, _ := proxy.User.Password()
	if proxy.User.Username() != "claude-bin" || password != claudeNativeTestToken {
		t.Fatal("proxy URL does not carry the scoped route identity")
	}
	if values["CLAUDE_CONFIG_DIR"] != stateDir {
		t.Fatal("native session state is not isolated")
	}
	if values["ANTHROPIC_CONFIG_DIR"] != stateDir {
		t.Fatal("native profile/config state is not isolated")
	}
	caPath := values["NODE_EXTRA_CA_CERTS"]
	if !filepath.IsAbs(caPath) || filepath.Dir(caPath) != stateDir {
		t.Fatalf("CA is not process-local and isolated: %q", caPath)
	}
	caInfo, err := os.Stat(caPath)
	if err != nil {
		t.Fatalf("CA file stat: %v", err)
	}
	if runtime.GOOS != "windows" && caInfo.Mode().Perm() != 0o600 {
		t.Fatalf("CA file mode = %v", caInfo.Mode().Perm())
	}
	caData, err := os.ReadFile(caPath)
	if err != nil || !bytes.Equal(caData, caPEM) || strings.Contains(string(caData), "PRIVATE KEY") {
		t.Fatalf("CA file content = %q err=%v", caData, err)
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(stateDir)
		if err != nil || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("state directory mode = %v err=%v", dirInfo, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("launcher manufactured onboarding completion state")
	}
	settings, err := os.ReadFile(filepath.Join(stateDir, claudeNativeSettings))
	if err != nil || !bytes.Contains(settings, []byte(`"forceLoginMethod": "claudeai"`)) {
		t.Fatalf("forced claude.ai login setting = %q err=%v", settings, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatal("helper created a vendor credential file")
	}
	if strings.Contains(strings.Join(got, "\n"), "vendor-secret") {
		t.Fatal("competing vendor credential leaked into the native environment")
	}
}

func TestClaudeNativeStateDirIsPrivateAndHashed(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", t.TempDir())
	root := "https://state.example.test/team/claude-bin"
	first, err := ClaudeNativeStateDir(root, claudeNativeTestToken)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ClaudeNativeStateDir(root, claudeNativeTestToken)
	if err != nil || first != second {
		t.Fatalf("state directory is not deterministic: %q %q %v", first, second, err)
	}
	if strings.Contains(first, claudeNativeTestToken) || strings.Contains(first, "state.example.test") {
		t.Fatal("state directory exposes a credential or proxy origin")
	}
	if filepath.Dir(filepath.Dir(first)) != filepath.Join(cache, "term-llm") || filepath.Base(filepath.Dir(first)) != "claude-proxy-v2" {
		t.Fatalf("unexpected state directory: %s", first)
	}
	other, err := ClaudeNativeStateDir(root, claudeNativeTestToken+"x")
	if err != nil || other == first {
		t.Fatal("rotated token reused the old state directory")
	}
	if _, err := ClaudeNativeStateDir("https://user:secret@state.example.test/route", claudeNativeTestToken); err == nil {
		t.Fatal("state directory accepted a credential-bearing root")
	}
}

func TestPrepareClaudeNativeEnvironmentReplacesRotatedCAAndKeepsSettings(t *testing.T) {
	firstCA := claudeNativeTestCA(t, nil)
	secondCA := claudeNativeTestCA(t, func(cert *x509.Certificate) {
		cert.Subject.CommonName = "rotated term-llm test Claude proxy"
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeNativeTestMetadata(secondCA, "http://"+r.Host, "claude-bin"))
	}))
	defer server.Close()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, claudeNativeCAFile), firstCA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, claudeNativeSettings), []byte(`{"existing":1,"forceLoginMethod":"claudeai"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := PrepareClaudeNativeEnvironment(context.Background(), server.URL+"/team/claude-bin", claudeNativeTestToken, stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	caPath := envEntryMap(got)["NODE_EXTRA_CA_CERTS"]
	data, err := os.ReadFile(caPath)
	if err != nil || !bytes.Equal(data, secondCA) {
		t.Fatalf("CA was not rotated: %v", err)
	}
	if info, err := os.Stat(caPath); err != nil {
		t.Fatalf("rotated CA stat: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("rotated CA mode = %v", info.Mode().Perm())
	}
	settings, err := os.ReadFile(filepath.Join(stateDir, claudeNativeSettings))
	if err != nil || !bytes.Contains(settings, []byte(`"existing":1`)) || !bytes.Contains(settings, []byte(`"forceLoginMethod":"claudeai"`)) {
		t.Fatalf("native settings were not preserved: %q err=%v", settings, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("launcher manufactured onboarding completion state")
	}
}

func TestPrepareClaudeNativeEnvironmentNoAmbientProxyOrRedirect(t *testing.T) {
	var ambientHits atomic.Int32
	ambient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ambientHits.Add(1)
		http.Error(w, "ambient proxy must not be used", http.StatusBadGateway)
	}))
	defer ambient.Close()
	t.Setenv("HTTP_PROXY", ambient.URL)
	t.Setenv("HTTPS_PROXY", ambient.URL)
	t.Setenv("ALL_PROXY", ambient.URL)

	var escapeHits atomic.Int32
	escape := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escapeHits.Add(1)
		t.Error("metadata redirect was followed")
	}))
	defer escape.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, escape.URL+"/secret-body", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := PrepareClaudeNativeEnvironment(context.Background(), redirect.URL+"/team/claude-bin", claudeNativeTestToken, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("redirect was not refused: %v", err)
	}
	if ambientHits.Load() != 0 || escapeHits.Load() != 0 {
		t.Fatalf("ambient proxy or redirect was used: ambient=%d escape=%d", ambientHits.Load(), escapeHits.Load())
	}
}

func TestPrepareClaudeNativeEnvironmentTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := PrepareClaudeNativeEnvironment(ctx, server.URL+"/team/claude-bin", claudeNativeTestToken, t.TempDir(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline was not surfaced: %v", err)
	}
}

func TestPrepareClaudeNativeEnvironmentRejectsLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges")
	}
	caPEM := claudeNativeTestCA(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeNativeTestMetadata(caPEM, "http://"+r.Host, "claude-bin"))
	}))
	defer server.Close()
	base := t.TempDir()
	t.Run("state directory", func(t *testing.T) {
		target := filepath.Join(base, "real-state")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "linked-state")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareClaudeNativeEnvironment(context.Background(), server.URL+"/team/claude-bin", claudeNativeTestToken, link, nil); err == nil {
			t.Fatal("symlinked state directory was accepted")
		}
	})
	t.Run("CA file", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(base, "outside-ca")
		if err := os.WriteFile(outside, caPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, claudeNativeCAFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareClaudeNativeEnvironment(context.Background(), server.URL+"/team/claude-bin", claudeNativeTestToken, dir, nil); err == nil {
			t.Fatal("symlinked CA file was accepted")
		}
	})
	t.Run("settings file", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(base, "outside-settings")
		if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, claudeNativeSettings)); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareClaudeNativeEnvironment(context.Background(), server.URL+"/team/claude-bin", claudeNativeTestToken, dir, nil); err == nil {
			t.Fatal("symlinked settings file was accepted")
		}
	})
}

func TestPrepareClaudeNativeEnvironmentMetadataErrorsAreSanitized(t *testing.T) {
	base := `{"protocol_version":2,"token_mode":"vault","hosts":["api.anthropic.com","platform.claude.com"],"certificate_pem":"secret-body","proxy_url":"$PROXY","route":"claude-bin"}`
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: "secret-body", want: "scoped client token"},
		{name: "forbidden", status: http.StatusForbidden, body: "secret-body", want: "scoped client token"},
		{name: "server error", status: http.StatusBadGateway, body: "secret-body " + claudeNativeTestToken, want: "HTTP 502"},
		{name: "invalid json", status: http.StatusOK, body: `{"certificate_pem":`, want: "invalid"},
		{name: "missing fields", status: http.StatusOK, body: `{"route":"claude-bin"}`, want: "missing"},
		{name: "old protocol", status: http.StatusOK, body: strings.Replace(base, `"protocol_version":2`, `"protocol_version":1`, 1), want: "protocol_version 2"},
		{name: "missing protocol", status: http.StatusOK, body: strings.Replace(base, `"protocol_version":2,`, ``, 1), want: "protocol_version 2"},
		{name: "wrong token mode", status: http.StatusOK, body: strings.Replace(base, `"vault"`, `"direct"`, 1), want: "token_mode"},
		{name: "missing hosts", status: http.StatusOK, body: strings.Replace(base, `"hosts":["api.anthropic.com","platform.claude.com"],`, ``, 1), want: "hosts must be exactly"},
		{name: "extra host", status: http.StatusOK, body: strings.Replace(base, `"platform.claude.com"]`, `"platform.claude.com","example.test"]`, 1), want: "hosts must be exactly"},
		{name: "duplicate host", status: http.StatusOK, body: strings.Replace(base, `"platform.claude.com"]`, `"api.anthropic.com"]`, 1), want: "hosts must be exactly"},
		{name: "bad route", status: http.StatusOK, body: strings.Replace(base, `"claude-bin"`, `"../secret"`, 1), want: "route slug"},
		{name: "bad proxy origin", status: http.StatusOK, body: strings.Replace(base, `"$PROXY"`, `"https://user:secret-body@proxy.example.test/path"`, 1), want: "proxy_url"},
		{name: "bad certificate", status: http.StatusOK, body: base, want: "certificate_pem"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, strings.ReplaceAll(tt.body, "$PROXY", "http://"+r.Host))
			}))
			defer server.Close()
			_, err := PrepareClaudeNativeEnvironment(context.Background(), server.URL+"/team/claude-bin", claudeNativeTestToken, t.TempDir(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			for _, secret := range []string{claudeNativeTestToken, "secret-body", "/team/claude-bin"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestPrepareClaudeNativeEnvironmentRejectsOversizeAndBadTokens(t *testing.T) {
	caPEM := claudeNativeTestCA(t, nil)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(claudeNativeTestMetadata(caPEM, "http://"+r.Host, "claude-bin"))
	}))
	defer server.Close()
	root := server.URL + "/team/claude-bin"
	for _, token := range []string{"", "sk-ant-vendor-secret", "admin_private", "has space", "has:colon", "has\ttab"} {
		if _, err := PrepareClaudeNativeEnvironment(context.Background(), root, token, t.TempDir(), nil); err == nil {
			t.Fatalf("token %q was accepted", token)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid tokens reached the proxy %d times", calls)
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", nativeMetadataLimit+1))
	}))
	defer large.Close()
	if _, err := PrepareClaudeNativeEnvironment(context.Background(), large.URL+"/team/claude-bin", claudeNativeTestToken, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversize metadata was accepted: %v", err)
	}
}

func TestClaudeNativeProxyURLChecksExactOrigin(t *testing.T) {
	root := "https://proxy.example.test:8443/team/claude-bin"
	for _, tt := range []struct {
		name, raw, want string
		ok              bool
	}{
		{name: "origin", raw: "https://proxy.example.test:8443", want: "https://proxy.example.test:8443", ok: true},
		{name: "trailing slash", raw: "https://proxy.example.test:8443/", want: "https://proxy.example.test:8443", ok: true},
		{name: "different port", raw: "https://proxy.example.test:9443"},
		{name: "different scheme", raw: "http://proxy.example.test:8443"},
		{name: "credentials", raw: "https://route:secret@proxy.example.test:8443"},
		{name: "query", raw: "https://proxy.example.test:8443?x=1"},
		{name: "fragment", raw: "https://proxy.example.test:8443#x"},
		{name: "path", raw: "https://proxy.example.test:8443/team"},
		{name: "opaque", raw: "https:proxy.example.test:8443"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := claudeNativePolicy.validateProxyURL(root, tt.raw)
			if tt.ok {
				if err != nil || got != tt.want {
					t.Fatalf("got %q, %v; want %q", got, err, tt.want)
				}
			} else if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid proxy URL accepted or leaked: %v", err)
			}
		})
	}
}

func TestClaudeNativeCARequiresSingleConstrainedPublicCA(t *testing.T) {
	valid := claudeNativeTestCA(t, nil)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret-private-key")})
	other := claudeNativeTestCA(t, func(cert *x509.Certificate) { cert.Subject.CommonName = "other" })
	invalidDER := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")})
	withHeaders := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: mustPEMBytes(t, valid)})
	for _, tt := range []struct {
		name string
		pem  []byte
		ok   bool
	}{
		{name: "valid", pem: valid, ok: true},
		{name: "valid with whitespace", pem: append([]byte("\n\t"), append(valid, '\n')...), ok: true},
		{name: "missing", pem: nil},
		{name: "private key only", pem: privateKey},
		{name: "certificate plus private key", pem: append(append([]byte{}, valid...), privateKey...)},
		{name: "two certificates", pem: append(append([]byte{}, valid...), other...)},
		{name: "invalid DER", pem: invalidDER},
		{name: "PEM headers", pem: withHeaders},
		{name: "leading garbage", pem: append([]byte("secret\n"), valid...)},
		{name: "trailing garbage", pem: append(append([]byte{}, valid...), []byte("secret\n")...)},
		{name: "not a CA", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.IsCA = false
			cert.KeyUsage = x509.KeyUsageDigitalSignature
			cert.MaxPathLenZero = false
			cert.MaxPathLen = 0
			cert.PermittedDNSDomainsCritical = false
			cert.PermittedDNSDomains = nil
			cert.ExcludedDNSDomains = nil
		})},
		{name: "cannot sign", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) { cert.KeyUsage = x509.KeyUsageDigitalSignature })},
		{name: "expired", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.NotBefore = time.Now().Add(-2 * time.Hour)
			cert.NotAfter = time.Now().Add(-time.Hour)
		})},
		{name: "too little validity", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) { cert.NotAfter = time.Now().Add(time.Hour) })},
		{name: "permitted elsewhere", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedDNSDomains = []string{"other.example.test"}
		})},
		{name: "permitted extra domain", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedDNSDomains = []string{claudeNativeAPIHost, "other.example.test"}
		})},
		{name: "permitted not critical", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedDNSDomainsCritical = false
		})},
		{name: "excluded API", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExcludedDNSDomains = []string{claudeNativeAPIHost}
		})},
		{name: "extra exclusion", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExcludedDNSDomains = []string{"." + claudeNativeAPIHost, ".example.test"}
		})},
		{name: "parent domain permitted", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedDNSDomains = []string{"anthropic.com"}
			cert.ExcludedDNSDomains = []string{".anthropic.com"}
		})},
		{name: "single host v1 CA", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedDNSDomains = []string{claudeNativeAPIHost}
			cert.ExcludedDNSDomains = []string{"." + claudeNativeAPIHost}
		})},
		{name: "subdomains excluded only", pem: claudeNativeTestCA(t, nil), ok: true},
		{name: "unconstrained", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedDNSDomainsCritical = false
			cert.PermittedDNSDomains = nil
			cert.ExcludedDNSDomains = nil
			cert.ExcludedIPRanges = nil
		})},
		{name: "missing IP exclusions", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExcludedIPRanges = nil
		})},
		{name: "IPv4 IP exclusion only", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExcludedIPRanges = []*net.IPNet{claudeNativeTestCIDR(t, "0.0.0.0/0")}
		})},
		{name: "extra IP exclusion", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExcludedIPRanges = append(cert.ExcludedIPRanges, claudeNativeTestCIDR(t, "10.0.0.0/8"))
		})},
		{name: "permitted IP range", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.PermittedIPRanges = []*net.IPNet{claudeNativeTestCIDR(t, "10.0.0.0/8")}
		})},
		{name: "wrong extended usage", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		})},
		{name: "missing extended usage", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.ExtKeyUsage = nil
		})},
		{name: "path length not zero", pem: claudeNativeTestCA(t, func(cert *x509.Certificate) {
			cert.MaxPathLenZero = false
			cert.MaxPathLen = 1
		})},
		{name: "not self-signed", pem: claudeNativeNotSelfSignedCA(t, valid)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := claudeNativePolicy.validateCA(string(tt.pem))
			if tt.ok {
				if err != nil {
					t.Fatalf("valid CA rejected: %v", err)
				}
				block, rest := pem.Decode(got)
				if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
					t.Fatalf("canonical CA is not one certificate: %q", got)
				}
			} else if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid CA accepted or leaked: %v", err)
			}
		})
	}
}

func mustPEMBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("invalid test PEM")
	}
	return block.Bytes
}

func claudeNativeNotSelfSignedCA(t *testing.T, valid []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(valid)
	template, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	parentKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	parentTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	parentDER, err := x509.CreateCertificate(rand.Reader, parentTemplate, parentTemplate, parentKey.Public(), parentKey)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := x509.ParseCertificate(parentDER)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, template.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestClaudeNativeEnvRejectsMissingStateDir(t *testing.T) {
	if _, err := PrepareClaudeNativeEnvironment(context.Background(), "https://proxy.example.test/claude-bin", claudeNativeTestToken, " ", nil); err == nil {
		t.Fatal("empty state directory was accepted")
	}
}

func TestPrepareClaudeNativeEnvironmentRefusesConflictingOrMalformedLoginSettings(t *testing.T) {
	caPEM := claudeNativeTestCA(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeNativeTestMetadata(caPEM, "http://"+r.Host, "claude-bin"))
	}))
	defer server.Close()
	root := server.URL + "/team/claude-bin"
	for name, body := range map[string]string{
		"conflicting method": `{"forceLoginMethod":"console"}`,
		"non-string method":  `{"forceLoginMethod":true}`,
		"malformed":          `{"forceLoginMethod":`,
		"duplicate":          `{"forceLoginMethod":"claudeai","forceLoginMethod":"console"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, claudeNativeSettings), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareClaudeNativeEnvironment(context.Background(), root, claudeNativeTestToken, dir, nil); err == nil {
				t.Fatal("unsafe settings file was overwritten")
			}
			if data, err := os.ReadFile(filepath.Join(dir, claudeNativeSettings)); err != nil || string(data) != body {
				t.Fatalf("settings changed after refusal: %q, %v", data, err)
			}
		})
	}
}

func TestPrepareClaudeNativeEnvironmentMergesLoginSettingWithoutLosingOtherSettings(t *testing.T) {
	caPEM := claudeNativeTestCA(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeNativeTestMetadata(caPEM, "http://"+r.Host, "claude-bin"))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, claudeNativeSettings)
	if err := os.WriteFile(path, []byte(`{"model":"sonnet","permissions":{"allow":["Bash"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClaudeNativeEnvironment(context.Background(), server.URL+"/team/claude-bin", claudeNativeTestToken, dir, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"model": "sonnet"`)) || !bytes.Contains(data, []byte(`"permissions"`)) || !bytes.Contains(data, []byte(`"forceLoginMethod": "claudeai"`)) {
		t.Fatalf("merged settings = %q", data)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("merged settings mode = %v, %v", info, err)
	}
}
