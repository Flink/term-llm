package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/fileprivacy"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

// Optional compatibility test against an explicitly selected installed Claude.
// Only get_usage is sent: no login, user prompt, inference or vendor network.
// HOME, config, environment and working directory are isolated synthetic state.
func TestClaudeForwardNativeBinaryUsage(t *testing.T) {
	binary := os.Getenv("TERM_LLM_TEST_CLAUDE_BINARY")
	if binary == "" {
		t.Skip("set TERM_LLM_TEST_CLAUDE_BINARY for isolated native compatibility verification")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native test executable must be an explicit absolute path")
	}
	s := testService(t)
	caDir := t.TempDir()
	if err := os.Chmod(caDir, 0700); err != nil {
		t.Fatal(err)
	}
	vendorCA, err := openClaudeForwardCA(caDir)
	if err != nil {
		t.Fatal(err)
	}
	var usageCalls, inferenceCalls atomic.Int32
	vendor := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != claudeAPIHost || r.Header.Get("Authorization") != "Bearer synthetic-native-vendor" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("native credential custody failed")
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/oauth/usage":
			usageCalls.Add(1)
			io.WriteString(w, `{"five_hour":{"utilization":12,"resets_at":"2030-01-01T00:00:00Z"},"seven_day":{"utilization":34,"resets_at":"2030-01-07T00:00:00Z"}}`)
		case "/api/oauth/profile":
			io.WriteString(w, `{"account":{"uuid":"11111111-1111-4111-8111-111111111111","email":"fixture@example.invalid","full_name":"Synthetic"},"organization":{"uuid":"22222222-2222-4222-8222-222222222222","organization_type":"claude_max","rate_limit_tier":"default_claude_max_5x"}}`)
		default:
			if r.URL.Path == "/v1/messages" {
				inferenceCalls.Add(1)
			}
			http.Error(w, "fixture endpoint not supported", 404)
		}
	}))
	vendor.TLS = &tls.Config{Certificates: []tls.Certificate{vendorCA.leaf}, MinVersion: tls.VersionTLS12}
	vendor.StartTLS()
	defer vendor.Close()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(vendorCA.certificatePEM))
	s.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	s.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != claudeAPIHost+":443" {
			return nil, fmt.Errorf("native fixture denied non-Anthropic destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, vendor.Listener.Addr().String())
	}
	a, key := seedReadyClaudeShared(t, s, "native", "synthetic-native-vendor", "synthetic-native-refresh")
	configure(t, s, Config{Routes: []Route{{Name: "native", LocalPrefix: "/native/", Upstream: "https://" + claudeAPIHost, Enabled: true, Account: "native", CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}, Allow: []string{"GET /api/oauth/usage", "GET /api/oauth/profile"}}}}, nil)
	token := caller(t, s, "native")
	scope := claudeSharedTestCallerScope(t, s, s.snap.routes[0], token)
	placeholder := claudeSharedAliasReply(key, a, scope, claudeVaultReply{})

	server := httptest.NewServer(s)
	defer server.Close()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	env, err := proxyclient.PrepareClaudeNativeEnvironment(ctx, server.URL+"/native", token, filepath.Join(home, "claude"), []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY=1"})
	if err != nil {
		t.Fatal(err)
	}
	// Seed only a synthetic native login holding opaque vault tokens. No real
	// credential is copied, and this compatibility check never performs login.
	credentials, err := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": placeholder.AccessToken, "refreshToken": placeholder.RefreshToken,
		"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
		"scopes":           []string{"user:profile", "user:inference"},
		"subscriptionType": "max", "rateLimitTier": "default_claude_max_5x",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fileprivacy.WriteFileAtomic(filepath.Join(home, "claude", ".credentials.json"), credentials); err != nil {
		t.Fatal(err)
	}
	if err := fileprivacy.WriteFileAtomic(filepath.Join(home, "claude", ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`)); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "-p", "--safe-mode", "--setting-sources", "user", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--tools", "", "--no-session-persistence")
	command.Env, command.Dir = env, home
	command.Stdin = strings.NewReader("{\"type\":\"control_request\",\"request_id\":\"fixture-usage\",\"request\":{\"subtype\":\"get_usage\"}}\n")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("isolated native usage failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if inferenceCalls.Load() != 0 {
		t.Fatal("usage attempted inference")
	}
	if usageCalls.Load() == 0 {
		t.Fatalf("native usage never reached proxy; stdout: %s stderr: %s", stdout.String(), stderr.String())
	}
	found := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		var reply struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Available bool `json:"rate_limits_available"`
					Limits    struct {
						FiveHour struct {
							Utilization float64 `json:"utilization"`
						} `json:"five_hour"`
					} `json:"rate_limits"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(line), &reply) == nil && reply.Type == "control_response" && reply.Response.RequestID == "fixture-usage" {
			if reply.Response.Subtype != "success" || !reply.Response.Response.Available || reply.Response.Response.Limits.FiveHour.Utilization != 12 {
				t.Fatalf("native usage did not preserve quota response: %s", line)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("native get_usage response missing: %s", stdout.String())
	}
}
