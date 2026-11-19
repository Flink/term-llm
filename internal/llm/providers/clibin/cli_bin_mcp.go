package clibin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mcphttp"
	"github.com/term-llm/term-llm/internal/runtimeoutput"
)

// cliMCPEndpoint is the bridge a CLI should see on this turn. Its zero value
// means no bridge, even if a tool-free ephemeral turn leaves one running.
type cliMCPEndpoint struct {
	url, token, configPath string
}

func (e cliMCPEndpoint) enabled() bool { return e.url != "" }

type cliMCPBridgeOptions struct {
	name     string
	enabled  bool
	specs    func([]core.ToolSpec) []mcphttp.ToolSpec // Defaults to mcpToolSpecs.
	executor mcphttp.ToolExecutor
	install  func(cliMCPEndpoint) (string, error) // Optional bridge-owned config file.
}

// cliMCPBridge owns the listener, credentials, and any config with the same
// lifetime as the listener. Adapters own native session rebinding and configs
// that must be written to a CLI home per turn.
type cliMCPBridge struct {
	mu                      sync.Mutex
	conversation, ephemeral cliMCPListener
	active                  atomic.Pointer[mcphttp.Server]
}

type cliMCPListener struct {
	server      *mcphttp.Server
	endpoint    cliMCPEndpoint
	fingerprint [sha256.Size]byte
}

func (b *cliMCPBridge) current() cliMCPEndpoint {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ephemeral.server != nil {
		return b.ephemeral.endpoint
	}
	return b.conversation.endpoint
}

func (b *cliMCPBridge) prepare(ctx context.Context, req core.Request, opts cliMCPBridgeOptions) (cliMCPEndpoint, error) {
	if len(req.Tools) == 0 && req.Ephemeral {
		return cliMCPEndpoint{}, nil
	}
	var retired cliMCPListener
	b.mu.Lock()
	defer func() {
		b.mu.Unlock()
		retired.close()
	}()
	listener := b.listenerFor(req)
	b.active.Store(nil)
	if len(req.Tools) == 0 || !opts.enabled {
		retired = listener.detach()
		if len(req.Tools) > 0 {
			runtimeoutput.Warn(opts.name+" tools requested but no tool executor configured", "tool_count", len(req.Tools))
		}
		return cliMCPEndpoint{}, nil
	}

	convert := opts.specs
	if convert == nil {
		convert = mcpToolSpecs
	}
	specs := convert(req.Tools)
	fingerprint, err := mcpToolSurfaceFingerprint(specs)
	if err != nil {
		retired = listener.detach()
		return cliMCPEndpoint{}, fmt.Errorf("prepare %s MCP tool surface: %w", opts.name, err)
	}
	if listener.server != nil && listener.fingerprint == fingerprint {
		b.active.Store(listener.server)
		return listener.endpoint, nil
	}

	next, err := b.start(ctx, req, opts, specs)
	// Start before retiring so a live-to-live rotation cannot reuse the port.
	// Retire even on failure: the old catalogue no longer matches this scope.
	retired = listener.detach()
	if err != nil {
		return cliMCPEndpoint{}, err
	}
	next.fingerprint = fingerprint
	*listener = next
	b.active.Store(next.server)
	return next.endpoint, nil
}

// A listener may authenticate while it is idle, but only the one selected for
// the current turn can send calls to the active provider stream.
type cliMCPAdmissionKey struct{}

func (b *cliMCPBridge) start(ctx context.Context, req core.Request, opts cliMCPBridgeOptions, specs []mcphttp.ToolSpec) (cliMCPListener, error) {
	if opts.executor == nil {
		return cliMCPListener{}, fmt.Errorf("start %s MCP bridge: missing executor", opts.name)
	}
	var server *mcphttp.Server
	admit := func() bool { return b.active.Load() == server }
	server = mcphttp.NewServer(func(ctx context.Context, name string, args json.RawMessage) (mcphttp.ToolResult, error) {
		if !admit() {
			return mcphttp.ToolResult{}, fmt.Errorf("MCP bridge is not active for this turn")
		}
		return opts.executor(context.WithValue(ctx, cliMCPAdmissionKey{}, admit), name, args)
	})
	server.SetDebug(req.Debug || req.DebugRaw)
	url, token, err := server.Start(ctx, specs)
	if err != nil {
		return cliMCPListener{}, fmt.Errorf("start %s MCP bridge: %w", opts.name, err)
	}
	listener := cliMCPListener{server: server, endpoint: cliMCPEndpoint{url: url, token: token}}
	if opts.install != nil {
		listener.endpoint.configPath, err = opts.install(listener.endpoint)
		if err != nil {
			listener.close()
			return cliMCPListener{}, fmt.Errorf("install %s MCP bridge config: %w", opts.name, err)
		}
	}
	return listener, nil
}

// The provider serializes turns, except Claude's bridge-free one-shots.
func (b *cliMCPBridge) finishTurn(req core.Request) {
	if req.Ephemeral && len(req.Tools) == 0 {
		return
	}
	b.mu.Lock()
	b.active.Store(nil)
	var retired cliMCPListener
	if req.Ephemeral {
		retired = b.ephemeral.detach()
	}
	b.mu.Unlock()
	retired.close()
}

func (b *cliMCPBridge) abortTurn(req core.Request) {
	b.mu.Lock()
	b.active.Store(nil)
	retired := b.listenerFor(req).detach()
	b.mu.Unlock()
	retired.close()
}

func (b *cliMCPBridge) listenerFor(req core.Request) *cliMCPListener {
	if req.Ephemeral {
		return &b.ephemeral
	}
	return &b.conversation
}

func (b *cliMCPBridge) close() {
	b.mu.Lock()
	b.active.Store(nil)
	ephemeral, conversation := b.ephemeral.detach(), b.conversation.detach()
	b.mu.Unlock()
	ephemeral.close()
	conversation.close()
}

func (l *cliMCPListener) detach() cliMCPListener {
	old := *l
	*l = cliMCPListener{}
	return old
}

func (l cliMCPListener) close() {
	stopMCPBridge(l.server, l.endpoint.configPath)
}

// Limit draining so a cancelled turn cannot hold up refresh or shutdown.
const mcpStopTimeout = 5 * time.Second

func stopMCPBridge(server *mcphttp.Server, configPath string) {
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), mcpStopTimeout)
		defer cancel()
		_ = server.Stop(ctx)
	}
	if configPath != "" {
		_ = os.Remove(configPath)
	}
}

// Fingerprint the actual registered specs, independent of the list order.
func mcpToolSurfaceFingerprint(specs []mcphttp.ToolSpec) ([sha256.Size]byte, error) {
	for _, spec := range specs {
		// The MCP SDK panics when registering a non-object input schema.
		if kind, _ := spec.Schema["type"].(string); kind != "object" {
			return [sha256.Size]byte{}, fmt.Errorf("tool %q requires an object input schema", spec.Name)
		}
	}
	sorted := slices.Clone(specs)
	slices.SortStableFunc(sorted, func(a, b mcphttp.ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	encoded, err := json.Marshal(sorted)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}
