package clibin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This deliberately exercises the direct CLI, not the term-llm proxy adapter.
// A transparent proxy of the exchange would give the worker the same access
// and refresh tokens this fixture sends. None of these values are real keys.
func TestRealCursorAPIKeyExchangeUsesSessionBearer(t *testing.T) {
	realCursor, jail := os.Getenv(cliProxyExecCursorEnv), os.Getenv(cliProxyExecJailEnv)
	if realCursor == "" || jail == "" {
		t.Skipf("set %s and %s to run the real cursor-agent against a synthetic gateway", cliProxyExecCursorEnv, cliProxyExecJailEnv)
	}
	work := t.TempDir()
	jailLog := writeCLIProxyExecWrapper(t, work, "cursor-agent", jail, realCursor)
	const apiKey, accessToken, refreshToken = "fixture-cursor-api-key", "fixture-cursor-access-token", "fixture-cursor-refresh-token"
	var exchanged, usedAccess atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backend/auth/exchange_user_api_key" && auth == "Bearer "+apiKey:
			exchanged.Store(true)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"accessToken":"`+accessToken+`","refreshToken":"`+refreshToken+`"}`)
		case r.Method == http.MethodPost && auth == "Bearer "+accessToken &&
			(strings.HasPrefix(r.URL.Path, "/backend/aiserver.v1.AiService/") ||
				strings.HasPrefix(r.URL.Path, "/backend/aiserver.v1.DashboardService/")):
			usedAccess.Store(true)
			(&cliProxyExecGateway{}).serveCursor(w, r, nil)
		default:
			t.Errorf("unexpected method, route or credential on %s %s", r.Method, r.URL.Path)
			http.Error(w, "not authorized by synthetic gateway", http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(work, "bin", "cursor-agent"),
		"--endpoint", server.URL+"/backend", "--agent-endpoint", server.URL+"/agent", "--repo-endpoint", server.URL+"/repo",
		"--http-version", "1.1", "--repo-http-version", "1.1", "models")
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + work, "TMPDIR=" + work,
		"XDG_CONFIG_HOME=" + filepath.Join(work, "xdg-config"), "XDG_CACHE_HOME=" + filepath.Join(work, "xdg-cache"),
		"CURSOR_CONFIG_DIR=" + filepath.Join(work, "config"), "CURSOR_DATA_DIR=" + filepath.Join(work, "data"),
		"NETJAIL_ALLOW_UNIX_PREFIX=" + work + string(os.PathSeparator),
		"AGENT_CLI_CREDENTIAL_STORE=memory", "CURSOR_API_KEY=" + apiKey,
	}
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "auto - Auto") {
		t.Fatalf("synthetic model listing failed: %v\n%s", err, output)
	}
	if !exchanged.Load() || !usedAccess.Load() {
		t.Fatal("the CLI did not exchange its API key and use the resulting access token")
	}
	assertCLIProxyExecContained(t, jailLog, true)
}
