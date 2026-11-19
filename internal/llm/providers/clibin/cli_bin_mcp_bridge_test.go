package clibin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mcphttp"
)

type testBridgeProvider interface {
	SetToolExecutor(func(context.Context, string, json.RawMessage) (core.ToolOutput, error))
	prepareMCPBridge(context.Context, core.Request) (cliMCPEndpoint, error)
	CleanupMCP()
}

func testMCPExecutor(context.Context, string, json.RawMessage) (core.ToolOutput, error) {
	return core.TextOutput("ok"), nil
}

func newClaudeBridgeTestProvider(t *testing.T) *ClaudeBinProvider {
	t.Helper()
	p := NewClaudeBinProvider("sonnet", nil)
	p.SetToolExecutor(testMCPExecutor)
	t.Cleanup(p.CleanupMCP)
	return p
}

func testMCPTool(name string) core.ToolSpec {
	return core.ToolSpec{Name: name, Schema: map[string]any{"type": "object"}}
}

func prepareTestBridge(t *testing.T, p testBridgeProvider, req core.Request) cliMCPEndpoint {
	t.Helper()
	endpoint, err := p.prepareMCPBridge(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestCLIMCPBridgeLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider testBridgeProvider
	}{
		{"agy-bin", NewAgyBinProvider("", nil)},
		{"claude-bin", NewClaudeBinProvider("sonnet", nil)},
		{"cursor-bin", NewCursorBinProvider("auto-smart", nil)},
		{"grok-bin", NewGrokBinProvider("", nil)},
		{"vibe-bin", NewVibeBinProvider("", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.provider
			p.SetToolExecutor(testMCPExecutor)
			t.Cleanup(p.CleanupMCP)
			alpha, beta, gamma := testMCPTool("alpha"), testMCPTool("beta"), testMCPTool("gamma")
			prepare := func(tools ...core.ToolSpec) cliMCPEndpoint {
				return prepareTestBridge(t, p, core.Request{Tools: tools})
			}
			first := prepare(alpha, beta)
			assertBridgeToolNames(t, first, "alpha", "beta")
			if got := prepare(beta, alpha); got != first {
				t.Fatal("reordering tools replaced the bridge")
			}
			if got := prepareTestBridge(t, p, core.Request{Ephemeral: true}); got.enabled() || prepare(alpha, beta) != first {
				t.Fatal("tool-free ephemeral turn exposed or replaced the conversation bridge")
			}

			second := prepare(alpha, beta, gamma)
			assertBridgeToolNames(t, second, "alpha", "beta", "gamma")
			assertBridgeRetired(t, first)
			third := prepare(alpha, beta)
			assertBridgeRetired(t, second)
			assertBridgeToolNames(t, third, "alpha", "beta")

			alpha.Description = "updated description"
			described := prepare(alpha, beta)
			assertBridgeRetired(t, third)
			if got := bridgeTools(t, described)["alpha"].Description; got != alpha.Description {
				t.Fatalf("tool description = %q, want %q", got, alpha.Description)
			}

			// Removing beta must revoke the old bridge even when the new alpha
			// schema cannot be encoded. A subsequent valid turn retries.
			bad := testMCPTool("alpha")
			bad.Schema["default"] = func() {}
			if got, err := p.prepareMCPBridge(context.Background(), core.Request{Tools: []core.ToolSpec{bad}}); got.enabled() || err == nil {
				t.Fatalf("failed refresh: bridge enabled=%t, err=%v", got.enabled(), err)
			}
			assertBridgeRetired(t, described)
			fourth := prepare(alpha)
			assertBridgeToolNames(t, fourth, "alpha")
			invalid := core.ToolSpec{Name: "invalid"}
			if got, err := p.prepareMCPBridge(context.Background(), core.Request{Tools: []core.ToolSpec{invalid}}); got.enabled() || err == nil {
				t.Fatalf("missing input schema: bridge enabled=%t, err=%v", got.enabled(), err)
			}
			assertBridgeRetired(t, fourth)
			fourth = prepare(alpha)

			if got := prepare(); got.enabled() {
				t.Fatal("tool-free conversation turn exposed a bridge")
			}
			assertBridgeRetired(t, fourth)
			last := prepare(alpha)
			if last.token == fourth.token {
				t.Fatal("re-enabled tools reused retired credentials")
			}
			assertBridgeToolNames(t, last, "alpha")
			p.CleanupMCP()
			assertBridgeRetired(t, last)
		})
	}
}

func TestCLIMCPBridgeFailedConfigInstallRevokesBothListeners(t *testing.T) {
	var bridge cliMCPBridge
	t.Cleanup(bridge.close)
	ctx := context.Background()
	opts := cliMCPBridgeOptions{name: "test", enabled: true, executor: func(context.Context, string, json.RawMessage) (mcphttp.ToolResult, error) {
		return mcphttp.ToolResult{}, nil
	}}
	old, err := bridge.prepare(ctx, core.Request{Tools: []core.ToolSpec{testMCPTool("alpha")}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	var replacement cliMCPEndpoint
	path := filepath.Join(t.TempDir(), "partial-config.json")
	wantErr := errors.New("config install failed")
	opts.install = func(endpoint cliMCPEndpoint) (string, error) {
		replacement = endpoint
		replacement.configPath = path
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return path, err
		}
		return path, wantErr
	}
	got, err := bridge.prepare(ctx, core.Request{Tools: []core.ToolSpec{testMCPTool("beta")}}, opts)
	if got.enabled() || !errors.Is(err, wantErr) || bridge.current().enabled() {
		t.Fatalf("failed install: bridge enabled=%t, err=%v", got.enabled(), err)
	}
	assertBridgeRetired(t, old)
	assertBridgeRetired(t, replacement)
}

func TestCLIMCPBridgeIsolatesToolBearingEphemeralTurns(t *testing.T) {
	var bridge cliMCPBridge
	t.Cleanup(bridge.close)
	ctx := context.Background()
	opts := cliMCPBridgeOptions{name: "test", enabled: true, executor: func(context.Context, string, json.RawMessage) (mcphttp.ToolResult, error) {
		return mcphttp.ToolResult{Content: "ok"}, nil
	}}
	prepare := func(req core.Request) cliMCPEndpoint {
		t.Helper()
		endpoint, err := bridge.prepare(ctx, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		return endpoint
	}
	parent := core.Request{Tools: []core.ToolSpec{testMCPTool("alpha")}}
	persistent := prepare(parent)
	for _, tool := range []string{"alpha", "beta"} {
		oneShot := core.Request{Ephemeral: true, Tools: []core.ToolSpec{testMCPTool(tool)}}
		temporary := prepare(oneShot)
		if temporary.token == persistent.token || temporary.url == persistent.url {
			t.Fatal("ephemeral turn reused parent credentials or listener")
		}
		assertBridgeCall(t, persistent, "alpha", false)
		assertBridgeCall(t, temporary, tool, true)
		bridge.finishTurn(oneShot)
		assertBridgeRetired(t, temporary)
		if got := prepare(parent); got != persistent {
			t.Fatal("ephemeral turn replaced the parent bridge")
		}
		assertBridgeCall(t, persistent, "alpha", true)
	}
	invalid := core.Request{Ephemeral: true, Tools: []core.ToolSpec{{Name: "invalid"}}}
	if _, err := bridge.prepare(ctx, invalid, opts); err == nil {
		t.Fatal("expected invalid ephemeral surface to fail")
	}
	bridge.finishTurn(invalid)
	if got := prepare(parent); got != persistent {
		t.Fatal("failed ephemeral setup replaced the parent bridge")
	}
}

func assertBridgeCall(t *testing.T, endpoint cliMCPEndpoint, tool string, wantSuccess bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session := connectTestBridge(t, ctx, endpoint)
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool})
	if err != nil {
		t.Fatalf("call bridge: %v", err)
	}
	if result.IsError == wantSuccess {
		t.Fatalf("bridge call IsError=%t, want success=%t", result.IsError, wantSuccess)
	}
}

type bridgeAuth struct {
	header string
	base   http.RoundTripper
}

func (a bridgeAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", a.header)
	return a.base.RoundTrip(req)
}

func connectTestBridge(t *testing.T, ctx context.Context, endpoint cliMCPEndpoint) *mcp.ClientSession {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint.url,
		HTTPClient:           &http.Client{Transport: bridgeAuth{header: "Bearer " + endpoint.token, base: transport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect to bridge: %v", err)
	}
	return session
}

func bridgeTools(t *testing.T, endpoint cliMCPEndpoint) map[string]*mcp.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session := connectTestBridge(t, ctx, endpoint)
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list bridge tools: %v", err)
	}
	tools := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func assertBridgeToolNames(t *testing.T, endpoint cliMCPEndpoint, want ...string) {
	t.Helper()
	var names []string
	for name := range bridgeTools(t, endpoint) {
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("bridge tools = %v, want %v", names, want)
	}
}

func assertBridgeRetired(t *testing.T, endpoint cliMCPEndpoint) {
	t.Helper()
	u, err := url.Parse(endpoint.url)
	if err != nil || u.Host == "" {
		t.Fatalf("invalid previous bridge endpoint %q: %v", endpoint.url, err)
	}
	if conn, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		conn.Close()
		t.Fatalf("previous MCP bridge %s still accepts connections", endpoint.url)
	}
	if endpoint.configPath != "" {
		if _, err := os.Stat(endpoint.configPath); !os.IsNotExist(err) {
			t.Fatalf("previous MCP config %q left behind: %v", endpoint.configPath, err)
		}
	}
}
