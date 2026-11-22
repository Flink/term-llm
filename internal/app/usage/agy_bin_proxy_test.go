package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

func stubAgyUsageConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	oldLoad, oldLook, oldDirect := loadProviderUsageConfig, agyUsageLookPath, agyUsageCommand
	t.Cleanup(func() {
		loadProviderUsageConfig, agyUsageLookPath, agyUsageCommand = oldLoad, oldLook, oldDirect
	})
	loadProviderUsageConfig = func() (*config.Config, error) { return cfg, nil }
}

func TestAgyBinUsageUsesConfiguredProxyBinding(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("AGY proxy workers require macOS or Linux")
	}

	hosts := []string{"oauth2.googleapis.com", "www.googleapis.com", "daily-cloudcode-pa.googleapis.com"}
	ca := usageProxyTestCA(t, hosts...)
	access := "agy-access-tlv2-" + strings.Repeat("a", 42) + "A"
	refresh := "agy-refresh-tlv2-" + strings.Repeat("b", 42) + "A"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/shared/.well-known/term-llm/agy-proxy" || r.Header.Get("Authorization") != "Bearer "+usageProxyTestToken {
			t.Error("unexpected AGY discovery request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 2, "token_mode": "vault", "hosts": hosts,
			"certificate_pem": string(ca), "proxy_url": "http://" + r.Host, "route": "shared",
			"access_placeholder": access, "refresh_placeholder": refresh, "expires_in": 3600,
		}); err != nil {
			t.Errorf("encode AGY discovery: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	root := server.URL + "/shared"
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: proxyclient.AgyNativeKind, Token: usageProxyTestToken}}); err != nil {
		t.Fatal(err)
	}
	stubAgyUsageConfig(t, &config.Config{Providers: map[string]config.ProviderConfig{
		"agy-bin": {ProxyBinding: root},
	}})
	agyUsageCommand = func(context.Context, string) ([]byte, error) {
		t.Fatal("configured binding fell back to the direct command")
		return nil, nil
	}

	fixtureDir := t.TempDir()
	t.Setenv("HOME", fixtureDir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TERM_LLM_CLI_WIRE_TRACE", "")
	t.Setenv("PATH", fixtureDir+":/usr/bin:/bin")
	// Distinct exit codes identify failed checks without printing proxy or OAuth values.
	script := `#!/bin/sh
set -eu
auth="$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
if [ "$#" -eq 1 ] && [ "$1" = --version ]; then
  [ ! -e "$auth" ] || exit 91
  : > "$HOME/version-checked"
  echo 1.5.13
  exit 0
fi
[ "$#" -eq 3 ] && [ "$1" = --output-format ] && [ "$2" = json ] && [ "$3" = -p=/usage ] || exit 92
[ -n "$HOME" ] && [ "$HOME" != "${0%/*}" ] && [ . -ef "$HOME/cwd" ] || exit 93
[ -f "$HOME/version-checked" ] || exit 94
case "$HTTPS_PROXY" in http://*) ;; *) exit 95 ;; esac
proxy_host="${HTTPS_PROXY##*@}"
proxy_host="${proxy_host#http://}"
case "$proxy_host" in 127.0.0.1:*) ;; *) exit 95 ;; esac
[ "$proxy_host" != '` + server.Listener.Addr().String() + `' ] || exit 95
grep -Fq '"access_token":"` + access + `"' "$auth" || exit 96
grep -Fq '"refresh_token":"` + refresh + `"' "$auth" || exit 97
cat <<'USAGE'
` + agyUsageFixture + `
USAGE
`
	if err := os.WriteFile(filepath.Join(fixtureDir, "agy"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runAgyBinUsage(context.Background(), &out, false); err != nil {
		t.Fatalf("runAgyBinUsage: %v", err)
	}
	for _, want := range []string{"Live through credential proxy", "Gemini Models", "25% used"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, &out)
		}
	}
}

func TestAgyBinUsageInvalidBindingNoFallback(t *testing.T) {
	stubAgyUsageConfig(t, &config.Config{Providers: map[string]config.ProviderConfig{
		"agy-bin": {ProxyBinding: "https://unregistered.example.test/agy"},
	}})
	agyUsageLookPath = func(string) (string, error) {
		t.Fatal("invalid binding must not look up agy")
		return "", nil
	}
	agyUsageCommand = func(context.Context, string) ([]byte, error) {
		t.Fatal("invalid binding fell back to the direct command")
		return nil, nil
	}
	err := runAgyBinUsage(context.Background(), &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "no direct fallback") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgyBinUsageWithoutBindingStaysDirect(t *testing.T) {
	stubAgyUsageConfig(t, &config.Config{})
	agyUsageLookPath = func(name string) (string, error) {
		if name != "agy" {
			t.Fatalf("look up %q, want agy", name)
		}
		return "/bin/agy", nil
	}
	var direct int
	agyUsageCommand = func(_ context.Context, binary string) ([]byte, error) {
		direct++
		if binary != "/bin/agy" {
			t.Fatalf("binary = %q, want /bin/agy", binary)
		}
		return []byte(agyUsageFixture), nil
	}
	var out bytes.Buffer
	if err := runAgyBinUsage(context.Background(), &out, false); err != nil {
		t.Fatalf("runAgyBinUsage: %v", err)
	}
	if direct != 1 || !strings.Contains(out.String(), "Live from agy") {
		t.Fatalf("direct=%d output=%s", direct, &out)
	}
}

func TestRedactAgyProxyToken(t *testing.T) {
	err := redactAgyProxyToken(errors.New("agy usage request failed: proxy http://r:"+usageProxyTestToken+"@h"), usageProxyTestToken)
	if strings.Contains(err.Error(), usageProxyTestToken) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("token not redacted: %v", err)
	}
}
