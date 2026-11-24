package mcpstart

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/mcp"
	mcpoauth "github.com/term-llm/term-llm/internal/mcp/oauth"
	"github.com/term-llm/term-llm/internal/testutil/mcptest"
	"golang.org/x/oauth2"
)

func TestMain(m *testing.M) {
	mcptest.RunIfChild()
	os.Exit(m.Run())
}

func runMCPStartOAuthTestIsolated(t *testing.T) bool {
	t.Helper()
	const childEnv = "TERM_LLM_TEST_MCPSTART_OAUTH"
	if os.Getenv(childEnv) == t.Name() {
		mcpoauth.DefaultCoordinator()
		return true
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"="+t.Name(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated OAuth test: %v\n%s", err, output)
	}
	return false
}

type stuckMCPStartup struct{}

func (stuckMCPStartup) ServerStatus(string) (mcp.ServerStatus, error) {
	return mcp.StatusStarting, nil
}

func TestWaitForMCPStartupBounded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    string
		wantErr error
	}{
		{name: "fallback deadline", context: func() (context.Context, context.CancelFunc) {
			return t.Context(), func() {}
		}, want: "MCP servers still starting after startup timeout: slow, stuck"},
		{name: "caller cancelled", context: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}, want: "MCP servers still starting: slow, stuck", wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.context()
			defer cancel()
			err := WaitForMCPStartup(ctx, stuckMCPStartup{}, []string{"slow", "stuck"}, io.Discard, false, 20*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("error = %v, want %q (cause %v)", err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestAskMCPStartup(t *testing.T) {
	const formerCutoff = 10 * time.Second
	for _, tc := range []struct {
		name            string
		delay, deadline time.Duration
		wantTimeout     bool
	}{
		// Cross ask's former cutoff while staying within the manager's budget.
		{name: "slow server becomes ready", delay: formerCutoff + time.Second, deadline: 20 * time.Second},
		{name: "deadline names the server", delay: time.Minute, deadline: 500 * time.Millisecond, wantTimeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), tc.deadline)
				defer cancel()
				server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "slow", Version: "1"}, nil)
				server.AddTool(&sdkmcp.Tool{Name: "test", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
					return &sdkmcp.CallToolResult{}, nil
				})
				handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true})
				firstRequest := make(chan struct{})
				var first sync.Once
				// Keep this test serial: the manager clones the process-wide
				// default transport. Restore it after the test server closes.
				originalTransport := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = originalTransport })
				httpServer := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					first.Do(func() {
						close(firstRequest)
						timer := time.NewTimer(tc.delay)
						defer timer.Stop()
						select {
						case <-timer.C:
						case <-r.Context().Done():
						case <-ctx.Done():
						}
					})
					if r.Context().Err() == nil && ctx.Err() == nil {
						handler.ServeHTTP(w, r)
					}
				}))
				// MCP replaces DialContext on its clone of DefaultTransport but
				// preserves DialTLSContext. Use the in-memory server's HTTPS dialer
				// so the real MCP exchange can run on synctest's clock.
				http.DefaultTransport = httpServer.Client().Transport
				endpoint := "https://" + strings.TrimPrefix(httpServer.URL, "http://")
				mcptest.WriteConfig(t, map[string]mcp.ServerConfig{"slow": {Type: "http", URL: endpoint}})

				var feedback bytes.Buffer
				var manager, startingManager *mcp.Manager
				var err error
				done := make(chan struct{})
				go func() {
					defer close(done)
					manager, err = EnableMCPServersWithFeedback(ctx, "slow", engine.NewEngine(nil, nil), &feedback, &MCPOptions{
						LoadConfig: func(m *mcp.Manager) error {
							startingManager = m
							return m.LoadConfig()
						},
					})
				}()
				defer func() {
					cancel()
					<-done
					if manager != nil {
						manager.StopAll()
					}
				}()

				synctest.Wait()
				select {
				case <-firstRequest:
				default:
					t.Fatal("background startup did not reach the MCP transport")
				}
				if !tc.wantTimeout {
					// The transport waits 11 seconds. At the former cutoff the
					// background startup must still be pending, as must the caller.
					synctest.Sleep(formerCutoff)
					select {
					case <-done:
						t.Fatalf("foreground startup returned before transport readiness: %v", err)
					default:
					}
					if status, err := startingManager.ServerStatus("slow"); status != mcp.StatusStarting || err != nil {
						t.Fatalf("background startup status = %v, %v; want starting", status, err)
					}
				}
				<-done
				if got := feedback.String(); strings.ContainsAny(got, "\r⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") || !strings.HasPrefix(got, "Starting MCP: slow\n") {
					t.Fatalf("non-terminal startup feedback = %q", got)
				}
				if tc.wantTimeout {
					if err == nil || !strings.Contains(err.Error(), "slow (startup timed out:") {
						t.Fatalf("error = %v, want named startup timeout", err)
					}
					if manager != nil {
						t.Fatal("failed startup returned a manager")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(feedback.String(), "✓ MCP ready: 1 tools from slow\n") {
						t.Fatalf("missing ready feedback: %q", feedback.String())
					}
					if manager == nil || len(manager.AllTools()) != 1 {
						t.Fatal("slow server's tools were not loaded")
					}
					if status, err := manager.ServerStatus("slow"); status != mcp.StatusReady || err != nil {
						t.Fatalf("background startup status = %v, %v; want ready", status, err)
					}
				}
			})
		})
	}
}

func TestCheckMCPServerFailures(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "ready", Version: "1"}, nil)
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" || r.URL.Path == "/other-auth" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	mcptest.WriteConfig(t, map[string]mcp.ServerConfig{})
	manager := mcp.NewManagerWithConfig(&mcp.Config{Servers: map[string]mcp.ServerConfig{
		"ready":      {URL: httpServer.URL + "/ready"},
		"failed":     {Command: filepath.Join(t.TempDir(), "missing-command")},
		"auth":       {URL: httpServer.URL + "/auth"},
		"other-auth": {URL: httpServer.URL + "/other-auth"},
	}})
	defer manager.StopAll()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, name := range manager.AvailableServers() {
		if err := manager.Enable(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := WaitForMCPStartup(ctx, manager, manager.AvailableServers(), io.Discard, false, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		servers []string
		want    []string
	}{
		{name: "ready", servers: []string{"ready"}},
		{name: "failed", servers: []string{"failed"}, want: []string{"MCP servers failed to start: failed ("}},
		{name: "auth required", servers: []string{"auth"}, want: []string{"MCP servers need sign-in: auth", "term-llm mcp login auth"}},
		{name: "multiple auth required", servers: []string{"auth", "other-auth"}, want: []string{"MCP servers need sign-in: auth, other-auth", "term-llm mcp login auth", "term-llm mcp login other-auth"}},
		{name: "ready and auth", servers: []string{"ready", "auth"}, want: []string{"MCP servers need sign-in: auth", "term-llm mcp login auth"}},
		{name: "mixed", servers: []string{"ready", "failed", "auth"}, want: []string{"MCP servers failed to start: failed (", "MCP servers need sign-in: auth", "term-llm mcp login auth"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var feedback bytes.Buffer
			err := CheckMCPServerFailures(manager, tc.servers, &feedback, true)
			if len(tc.want) == 0 {
				if err != nil || feedback.Len() != 0 {
					t.Fatalf("ready check: err=%v feedback=%q", err, feedback.String())
				}
				return
			}
			if err == nil {
				t.Fatal("expected startup error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, missing %q", err.Error(), want)
				}
			}
			if feedback.String() != "\n" {
				t.Errorf("animated error feedback = %q", feedback.String())
			}
		})
	}
}

func TestEnableMCPServersWithFeedbackRequiresSignIn(t *testing.T) {
	protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer protected.Close()
	for _, selection := range []string{"protected", "greeter,protected"} {
		t.Run(selection, func(t *testing.T) {
			mcptest.WriteConfig(t, map[string]mcp.ServerConfig{
				"protected": {URL: protected.URL},
				"greeter":   {Command: os.Args[0], Env: map[string]string{mcptest.ChildEnv: "1"}},
			})
			var feedback bytes.Buffer
			manager, err := EnableMCPServersWithFeedback(t.Context(), selection, engine.NewEngine(nil, engine.NewToolRegistry()), &feedback, nil)
			if manager != nil {
				defer manager.StopAll()
				t.Error("startup with missing authorization returned a manager")
			}
			if err == nil || !strings.Contains(err.Error(), "MCP servers need sign-in: protected") || !strings.Contains(err.Error(), "term-llm mcp login protected") {
				t.Fatalf("startup error = %v", err)
			}
			if strings.Contains(feedback.String(), "✓ MCP ready") {
				t.Fatalf("misleading ready feedback: %q", feedback.String())
			}
		})
	}
}

func TestCheckMCPServerFailuresInsufficientScope(t *testing.T) {
	if !runMCPStartOAuthTestIsolated(t) {
		return
	}
	path, err := mcpoauth.DefaultStorePath()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope"`)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	_, err = mcpoauth.NewFileStore(path).Update(server.URL, func(*mcpoauth.Session) (*mcpoauth.Session, error) {
		return &mcpoauth.Session{Endpoint: server.URL, Config: mcpoauth.OAuth2Config{ClientID: "client"}, Token: &oauth2.Token{AccessToken: "valid", Expiry: time.Now().Add(time.Hour)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := mcp.NewManagerWithConfig(&mcp.Config{Servers: map[string]mcp.ServerConfig{"protected": {URL: server.URL}}})
	defer manager.StopAll()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := manager.Enable(ctx, "protected"); err != nil {
		t.Fatal(err)
	}
	if err := WaitForMCPStartup(ctx, manager, []string{"protected"}, io.Discard, false, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if manager.AuthStatuses()["protected"].State != mcpoauth.AuthSignedIn {
		t.Fatal("scope challenge invalidated stored grant")
	}
	err = CheckMCPServerFailures(manager, []string{"protected"}, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "term-llm mcp login --force protected") {
		t.Fatalf("scope step-up hint = %v", err)
	}
}
