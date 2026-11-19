package proxyrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/mitmca"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

var (
	agyTestHosts   = []string{"oauth2.googleapis.com", "www.googleapis.com", "daily-cloudcode-pa.googleapis.com"}
	agyTestAccess  = "agy-access-tlv2-" + strings.Repeat("a", 42) + "A"
	agyTestRefresh = "agy-refresh-tlv2-" + strings.Repeat("b", 42) + "A"
)

func TestRunAgyScopedLogin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only native storage")
	}
	home, _, work := isolateLauncher(t)
	ca := testCA(t, agyTestHosts...)
	var discoveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personal/.well-known/term-llm/agy-proxy" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("incorrect agy metadata request")
		}
		metadata := map[string]any{
			"protocol_version": 2, "token_mode": "vault", "hosts": agyTestHosts,
			"certificate_pem": string(ca), "proxy_url": "http://" + r.Host, "route": "personal",
		}
		if discoveries.Add(1) == 3 { // Stale version probe, then enrollment and its confirmation.
			metadata["access_placeholder"], metadata["refresh_placeholder"], metadata["expires_in"] = agyTestAccess, agyTestRefresh, 3600
		}
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	root := server.URL + "/personal"
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Token: testToken, Kind: "agy-bin"}}); err != nil {
		t.Fatal(err)
	}
	loadConfig := func() (*config.Config, error) {
		return &config.Config{Providers: map[string]config.ProviderConfig{"agy-bin": {ProxyBinding: root}}}, nil
	}
	var out bytes.Buffer
	opts := Options{LoadConfig: loadConfig, Out: &out, Err: io.Discard, Args: []string{"acp", "--login"},
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "DBUS_SESSION_BUS_ADDRESS=real-keyring", "GOOGLE_APPLICATION_CREDENTIALS=real-google", "NO_PROXY=*", "BROWSER=ambient-browser", "DISPLAY=:0", "SSL_CERT_DIR=/ambient/trust", "SSL_CERT_FILE=/ambient/ca"},
	}
	opts.Command = fakeClaude(t, work, "if [ \"$1\" = --version ]; then echo 1.5.11; exit 0; fi\necho unexpected\n")
	if err := RunAgy(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "1.5.12") || out.Len() != 0 {
		t.Fatalf("stale binary was not rejected before login: %v", err)
	}
	opts.Command = fakeClaude(t, work, `
if [ "$1" = --version ]; then echo 1.5.12; exit 0; fi
set -eu
[ "$1 $2" = 'acp --login' ]
[ -z "${DBUS_SESSION_BUS_ADDRESS:-}${GOOGLE_APPLICATION_CREDENTIALS:-}${DISPLAY:-}" ]
[ "$BROWSER" = /bin/false ]
[ "$NO_PROXY" != '*' ]
[ "$(pwd)" = "$HOME" ]
[ -z "${SSL_CERT_DIR:-}" ]
[ "$SSL_CERT_FILE" = "${HOME%/*}/agy-launcher-trust.pem" ]
[ -f "$SSL_CERT_FILE" ]
case "$HTTPS_PROXY" in http://personal:sk-tlp-*@127.0.0.1:*) ;; *) exit 9 ;; esac
IFS= read -r pasted
[ "$pasted" = 'test-code' ]
[ ! -e "$HOME/.gemini/antigravity-cli/antigravity-oauth-token" ]
echo logged-in
`)
	opts.In = strings.NewReader("test-code\n")
	if err := RunAgy(context.Background(), opts); err != nil || out.String() != "logged-in\n" {
		t.Fatalf("fixture login: %v, %q", err, out.String())
	}
	out.Reset()
	opts.In = strings.NewReader("test-code\n")
	if err := RunAgy(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "proxy has not confirmed login") || out.String() != "logged-in\n" {
		t.Fatalf("local success without proxy confirmation: %v, %q", err, out.String())
	}
	if discoveries.Load() != 5 {
		t.Fatal("operator login did not re-fetch discovery")
	}
	state, err := proxyclient.AgyNativeStateDir(root, testToken)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(state, "agy-launcher-trust.pem"))
	if err != nil || !bytes.Equal(bundle, mitmca.SystemRootsBundle(ca)) {
		t.Fatal("launcher trust did not combine the scoped CA and system bundle", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("native launch wrote into the caller's real HOME")
	}
	if homes, err := filepath.Glob(filepath.Join(state, "launcher-*")); err != nil || len(homes) != 0 {
		t.Fatalf("operator launcher left a private profile: %v", err)
	}
}

func TestRunAgySharedUsesFreshProfileWithoutLogin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only native storage")
	}
	home, _, work := isolateLauncher(t)
	ca := testCA(t, agyTestHosts...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shared/.well-known/term-llm/agy-proxy" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("incorrect agy metadata request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 2, "token_mode": "vault", "hosts": agyTestHosts,
			"certificate_pem": string(ca), "proxy_url": "http://" + r.Host, "route": "shared",
			"access_placeholder": agyTestAccess, "refresh_placeholder": agyTestRefresh, "scopes": "discovery-only", "expires_in": 3600,
		})
	}))
	defer server.Close()
	var out bytes.Buffer
	opts := Options{URL: server.URL + "/shared", Token: testToken, Out: &out, Err: io.Discard, Args: []string{"models"}}
	opts.Command = fakeClaude(t, work, `
if [ "$1" = --version ]; then echo 1.5.12; exit 0; fi
set -eu
[ "$*" = models ]
[ "$(pwd)" = "$HOME" ]
[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}${DBUS_SESSION_BUS_ADDRESS:-}" ]
auth="$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
grep -q '"access_token":"`+agyTestAccess+`"' "$auth"
grep -q '"refresh_token":"`+agyTestRefresh+`"' "$auth"
! grep -q 'discovery-only' "$auth"
printf '%s\n' "$HOME"
`)
	var first string
	for range 2 {
		out.Reset()
		if err := RunAgy(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		childHome := strings.TrimSpace(out.String())
		if childHome == "" || childHome == first || childHome == home {
			t.Fatal("worker launcher did not receive a distinct private profile")
		}
		if _, err := os.Stat(childHome); !os.IsNotExist(err) {
			t.Fatal("worker launcher profile was not removed")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(childHome), "login")); !os.IsNotExist(err) {
			t.Fatal("worker launcher wrote an operator profile")
		}
		first = childHome
	}
	out.Reset()
	opts.Args = []string{"acp", "--disable-telemetry", "--login"}
	if err := RunAgy(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "omit --login") || out.Len() != 0 {
		t.Fatalf("worker login was not rejected: %v", err)
	}
}

func TestAgySharedOutputCapturesStdoutInPrivateProfile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only native storage")
	}
	home, _, work := isolateLauncher(t)
	ca := testCA(t, agyTestHosts...)
	shared := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]any{
			"protocol_version": 2, "token_mode": "vault", "hosts": agyTestHosts,
			"certificate_pem": string(ca), "proxy_url": "http://" + r.Host, "route": "shared",
		}
		if shared {
			metadata["access_placeholder"], metadata["refresh_placeholder"], metadata["expires_in"] = agyTestAccess, agyTestRefresh, 3600
		}
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	connection := Connection{Root: server.URL + "/shared", Token: testToken}
	binary := fakeClaude(t, work, `
if [ "$1" = --version ]; then echo 1.5.12; exit 0; fi
set -eu
[ "$*" = '--output-format json -p=/usage' ]
[ "$(pwd)" = "$HOME" ]
case "$HTTPS_PROXY" in http://shared:sk-tlp-*@127.0.0.1:*) ;; *) exit 9 ;; esac
grep -q '"access_token":"`+agyTestAccess+`"' "$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
echo "leaked $HTTPS_PROXY" >&2
printf '{"status":"success","home":"%s"}\n' "$HOME"
`)
	out, err := AgySharedOutput(context.Background(), connection, binary, "--output-format", "json", "-p=/usage")
	if err != nil {
		t.Fatal(err)
	}
	var reply struct{ Status, Home string }
	if err := json.Unmarshal(out, &reply); err != nil || reply.Status != "success" {
		t.Fatalf("stdout = %q, err = %v", out, err)
	}
	if reply.Home == "" || reply.Home == home {
		t.Fatal("shared output did not use a private profile")
	}
	if _, err := os.Stat(reply.Home); !os.IsNotExist(err) {
		t.Fatal("shared output profile was not removed")
	}

	failing := fakeClaude(t, work, "if [ \"$1\" = --version ]; then echo 1.5.12; exit 0; fi\necho \"$HTTPS_PROXY\" >&2\nexit 3\n")
	if _, err := AgySharedOutput(context.Background(), connection, failing, "-p=/usage"); err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("native failure leaked or succeeded: %v", err)
	}

	shared = false
	if _, err := AgySharedOutput(context.Background(), connection, binary, "-p=/usage"); err == nil || !strings.Contains(err.Error(), "no shared sign-in") {
		t.Fatalf("missing shared identity was not rejected: %v", err)
	}
}
