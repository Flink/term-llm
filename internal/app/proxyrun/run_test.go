package proxyrun

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
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const testToken = "sk-tlp-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatal(err)
	}
	return network
}

func testCA(t *testing.T, hosts ...string) []byte {
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
	if len(hosts) == 0 {
		hosts = []string{"api.anthropic.com", "platform.claude.com"}
	}
	var excluded []string
	for _, host := range hosts {
		excluded = append(excluded, "."+host)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "term-llm proxyrun test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         hosts,
		ExcludedDNSDomains:          excluded,
		ExcludedIPRanges:            []*net.IPNet{testCIDR(t, "0.0.0.0/0"), testCIDR(t, "::/0")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func metadataServer(t *testing.T) *httptest.Server {
	t.Helper()
	caPEM := testCA(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/.well-known/term-llm/claude-proxy") || r.URL.RawQuery != "" {
			t.Errorf("unexpected bootstrap target: %s", r.URL)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 2, "token_mode": "vault",
			"hosts":           []string{"api.anthropic.com", "platform.claude.com"},
			"certificate_pem": string(caPEM), "proxy_url": "http://" + r.Host, "route": "claude-bin",
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func fakeClaude(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func envMap(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	return values
}

func isolateLauncher(t *testing.T) (home, cache, work string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell CLI fixture")
	}
	home, cache, work = t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", cache)
	return home, cache, work
}

func TestRunPreparesProcessEnvironmentForwardsArgumentsAndLeavesHomeClaudeUntouched(t *testing.T) {
	home, cache, work := isolateLauncher(t)
	server := metadataServer(t)
	root := server.URL + "/team/claude-bin"
	argsPath, envPath := filepath.Join(work, "args"), filepath.Join(work, "env")
	command := fakeClaude(t, work, `printf '%s\n' "$@" > "$ARGS_FILE"
env > "$ENV_FILE"
exit 0
`)
	homeClaude := filepath.Join(home, ".claude")
	if err := os.Mkdir(homeClaude, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsBefore := []byte(`{"model":"sonnet","env":{"CLAUDE_CODE_OAUTH_TOKEN":"real-login"}}`)
	credentialsBefore := []byte(`{"accessToken":"real-credential"}`)
	if err := os.WriteFile(filepath.Join(homeClaude, "settings.json"), settingsBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeClaude, ".credentials.json"), credentialsBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CACHE_HOME=" + cache,
		"ARGS_FILE=" + argsPath, "ENV_FILE=" + envPath, "PRESERVED=yes",
		"CLAUDE_CODE_OAUTH_TOKEN=real-oauth-canary", "CLAUDE_CODE_OAUTH_SCOPES=user:inference",
		"ANTHROPIC_API_KEY=real-key-canary", "ANTHROPIC_CONFIG_DIR=/ambient/anthropic",
		"ANTHROPIC_PROFILE=real-profile", "CLAUDE_CODE_USE_BEDROCK=1",
		"HTTPS_PROXY=http://ambient.example.test:8080", "https_proxy=http://ambient.example.test:8080",
		"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=0",
		"NO_PROXY=api.anthropic.com,platform.claude.com,corp.internal",
	}
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{
		URL: root, Token: testToken, Command: command,
		Args: []string{"--resume", "-p", "hello world"},
		Out:  &out, Err: &errOut, Env: env,
	})
	if err != nil {
		t.Fatalf("run: %v (stderr %q)", err, errOut.String())
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "--resume\n-p\nhello world\n" {
		t.Fatalf("native arguments = %q", args)
	}
	values := envMap(t, envPath)
	if values["CLAUDE_CODE_OAUTH_TOKEN"] != "" || values["CLAUDE_CODE_OAUTH_SCOPES"] != "" || values["ANTHROPIC_API_KEY"] != "" || values["ANTHROPIC_PROFILE"] != "" || values["CLAUDE_CODE_USE_BEDROCK"] != "" {
		t.Fatal("competing vendor identity reached the native client")
	}
	if values["PRESERVED"] != "yes" {
		t.Fatal("unrelated environment was not preserved")
	}
	expectedState, err := proxyclient.ClaudeNativeStateDir(root, testToken)
	if err != nil {
		t.Fatal(err)
	}
	if values["CLAUDE_CONFIG_DIR"] != expectedState || values["ANTHROPIC_CONFIG_DIR"] != expectedState {
		t.Fatalf("native config dirs = %q/%q", values["CLAUDE_CONFIG_DIR"], values["ANTHROPIC_CONFIG_DIR"])
	}
	if values["TERM_LLM_CLAUDE_PROXY_ROOT"] != "" {
		t.Fatal("internal routing marker leaked into the native environment")
	}
	if values["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"] != "1" {
		t.Fatal("Claude settings can override the launcher’s proxy")
	}
	if _, ok := values["https_proxy"]; ok || values["HTTP_PROXY"] != values["HTTPS_PROXY"] {
		t.Fatal("competing proxy environment reached the native client")
	}
	proxy, err := url.Parse(values["HTTPS_PROXY"])
	if err != nil || proxy.User == nil || proxy.User.Username() != "claude-bin" || proxy.Scheme+"://"+proxy.Host != server.URL || proxy.Path != "" {
		t.Fatal("Claude HTTPS proxy does not point at the scoped proxy origin")
	}
	password, _ := proxy.User.Password()
	if password != testToken {
		t.Fatal("proxy URL does not carry the scoped client token")
	}
	noProxy := strings.ToLower(values["NO_PROXY"])
	if noProxy != strings.ToLower(values["no_proxy"]) || strings.Contains(noProxy, "api.anthropic.com") || strings.Contains(noProxy, "platform.claude.com") || !strings.Contains(noProxy, "corp.internal") {
		t.Fatalf("NO_PROXY = %q", values["NO_PROXY"])
	}
	settings, err := os.ReadFile(filepath.Join(expectedState, "settings.json"))
	if err != nil || !bytes.Contains(settings, []byte(`"forceLoginMethod": "claudeai"`)) {
		t.Fatalf("isolated settings = %q, %v", settings, err)
	}
	if _, err := os.Stat(filepath.Join(expectedState, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("launcher manufactured onboarding completion state")
	}
	settingsAfter, err := os.ReadFile(filepath.Join(homeClaude, "settings.json"))
	if err != nil || !bytes.Equal(settingsAfter, settingsBefore) {
		t.Fatalf("normal ~/.claude/settings.json changed: %q, %v", settingsAfter, err)
	}
	credentialsAfter, err := os.ReadFile(filepath.Join(homeClaude, ".credentials.json"))
	if err != nil || !bytes.Equal(credentialsAfter, credentialsBefore) {
		t.Fatalf("normal ~/.claude credential changed: %q, %v", credentialsAfter, err)
	}
}

func TestResolveConfiguredConnectionUsesMaterializedProviderProxy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "term-llm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	root := "https://provider-local.example.test/claude-bin"
	configYAML := "providers:\n  claude-bin:\n    proxy:\n      url: " + root + "\n      token: " + testToken + "\n"
	if err := os.WriteFile(path, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection, configured, err := ResolveConfiguredConnection(cfg)
	if err != nil || !configured || connection.Root != root || connection.Token != testToken {
		t.Fatalf("configured connection = %+v, configured=%v, err=%v", connection, configured, err)
	}
}

func TestResolveConfiguredConnectionForNamedAlias(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "term-llm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	root := "https://provider-alias.example.test/work-claude"
	configYAML := "providers:\n  work-claude:\n    type: claude-bin\n    proxy:\n      url: " + root + "\n      token: " + testToken + "\n"
	if err := os.WriteFile(path, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection, configured, err := ResolveConfiguredConnectionFor(cfg, "work-claude")
	if err != nil || !configured || connection.Root != root || connection.Token != testToken {
		t.Fatalf("alias connection = %+v, configured=%v, err=%v", connection, configured, err)
	}
	if _, configured, err := ResolveConfiguredConnection(cfg); err != nil || configured {
		t.Fatalf("default claude-bin unexpectedly configured: configured=%v err=%v", configured, err)
	}
}

func TestRunPropagatesChildExitCodeAndCancellation(t *testing.T) {
	_, _, work := isolateLauncher(t)
	server := metadataServer(t)
	root := server.URL + "/claude-bin"
	command := fakeClaude(t, work, "exit 7\n")
	err := Run(context.Background(), Options{URL: root, Token: testToken, Command: command, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: []string{"PATH=" + os.Getenv("PATH")}})
	var exitErr *ChildExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("child exit = %v", err)
	}
	slow := fakeClaude(t, work, "exec sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err = Run(ctx, Options{URL: root, Token: testToken, Command: slow, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: []string{"PATH=" + os.Getenv("PATH")}})
	if !errors.As(err, &exitErr) || exitErr.Code != 130 {
		t.Fatalf("cancellation exit = %v", err)
	}
}

func TestRunMalformedSRVFailsBeforeCredentialConfigOrPreparation(t *testing.T) {
	t.Setenv("EMPTY_PROXY_SRV_TEST_TOKEN", "")
	for name, run := range map[string]func(context.Context, Options) error{"claude": Run, "agy": RunAgy} {
		t.Run(name, func(t *testing.T) {
			err := run(t.Context(), Options{
				URL: "srv://invalid", Token: "${EMPTY_PROXY_SRV_TEST_TOKEN}", Command: "should-not-run",
				LoadConfig: func() (*config.Config, error) {
					t.Fatal("explicit SRV URL must not load config")
					return nil, nil
				},
				Prepare: func(context.Context, *exec.Cmd, string, string, string) (func(), error) {
					t.Fatal("invalid SRV URL must not prepare a command")
					return nil, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), "srv://_service._tcp.domain") {
				t.Fatalf("expected an SRV validation error before token resolution, got %v", err)
			}
		})
	}
}

func TestRunClaudePreparedCommandCleanupOffline(t *testing.T) {
	_, _, work := isolateLauncher(t)
	for _, tt := range []struct {
		name, command string
		success       bool
	}{
		{"child exits", fakeClaude(t, work, `printf '%s' "$PREPARED"`), true},
		{"start fails", filepath.Join(work, "missing"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cleaned := false
			err := Run(t.Context(), Options{
				URL: "https://proxy.example.test/claude", Token: testToken, Command: tt.command,
				Out: &out, Env: []string{"PRESERVED=1"},
				Prepare: func(_ context.Context, cmd *exec.Cmd, _, _, _ string) (func(), error) {
					cmd.Env = append(cmd.Env, "PREPARED=ready")
					return func() { cleaned = true }, nil
				},
			})
			if (err == nil) != tt.success || !cleaned {
				t.Fatalf("launch error = %v; cleaned = %v", err, cleaned)
			}
			if tt.success && out.String() != "ready" {
				t.Fatalf("child output = %q, want prepared environment", out.String())
			}
		})
	}
}

func TestRunRequiresExplicitPairOrConfiguredBindingWithoutFallback(t *testing.T) {
	_, _, work := isolateLauncher(t)
	server := metadataServer(t)
	root := server.URL + "/claude-bin"
	marker := filepath.Join(work, "executed")
	command := fakeClaude(t, work, "printf ran > \"$EXEC_MARKER\"\nexit 0\n")
	for _, opts := range []Options{
		{URL: root},
		{Token: testToken},
		{LoadConfig: func() (*config.Config, error) { return &config.Config{}, nil }},
	} {
		opts.Command, opts.Out, opts.Err = command, &bytes.Buffer{}, &bytes.Buffer{}
		opts.Env = []string{"PATH=" + os.Getenv("PATH"), "EXEC_MARKER=" + marker}
		if err := Run(context.Background(), opts); err == nil {
			t.Fatal("launcher silently fell back without an explicit pair or configured binding")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("native client executed after configuration failure")
		}
	}
}

func TestRunResolvesConfiguredBindingAndTokenReference(t *testing.T) {
	_, _, work := isolateLauncher(t)
	server := metadataServer(t)
	root := server.URL + "/claude-bin"
	argsPath := filepath.Join(work, "args")
	command := fakeClaude(t, work, "printf '%s\\n' \"$HTTPS_PROXY\" > \"$ARGS_FILE\"\nexit 0\n")
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: "claude-bin", Token: testToken}}); err != nil {
		t.Fatal(err)
	}
	load := func() (*config.Config, error) {
		return &config.Config{Providers: map[string]config.ProviderConfig{"claude-bin": {ProxyBinding: root}}}, nil
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "ARGS_FILE=" + argsPath}
	if err := Run(context.Background(), Options{Command: command, LoadConfig: load, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: env}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := url.Parse(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if password, _ := proxy.User.Password(); password != testToken {
		t.Fatal("configured binding did not provide the scoped client token")
	}

	tokenFile := filepath.Join(work, "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), Options{
		URL: root, Token: "file://" + tokenFile, Command: command,
		Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: env,
	}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err = url.Parse(strings.TrimSpace(string(data)))
	if err != nil || proxy.User == nil {
		t.Fatalf("token reference proxy URL = %q, %v", data, err)
	}
	if password, _ := proxy.User.Password(); password != testToken {
		t.Fatal("credential reference was not resolved")
	}
}

func TestRunConfiguredInvalidBindingFailsBeforeExec(t *testing.T) {
	_, _, work := isolateLauncher(t)
	marker := filepath.Join(work, "executed")
	command := fakeClaude(t, work, "printf ran > \"$EXEC_MARKER\"\nexit 0\n")
	for _, binding := range []proxyclient.Binding{
		{Root: "https://invalid-kind.example.test/claude-bin", Kind: "chatgpt", Token: testToken},
		{Root: "https://invalid-token.example.test/claude-bin", Kind: "claude-bin", Token: "sk-ant-vendor-secret"},
	} {
		if err := proxyclient.Register([]proxyclient.Binding{binding}); err != nil {
			t.Fatal(err)
		}
		load := func() (*config.Config, error) {
			return &config.Config{Providers: map[string]config.ProviderConfig{"claude-bin": {ProxyBinding: binding.Root}}}, nil
		}
		err := Run(context.Background(), Options{Command: command, LoadConfig: load, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: []string{"PATH=" + os.Getenv("PATH"), "EXEC_MARKER=" + marker}})
		if err == nil {
			t.Fatal("invalid configured binding was accepted")
		}
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatal("invalid configured binding fell back to the native login")
		}
	}
}
