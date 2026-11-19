package clibin

import (
	"context"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func (p *CursorBinProvider) prepareMCPBridge(ctx context.Context, req core.Request) (cliMCPEndpoint, error) {
	return p.mcpBridge.prepare(ctx, req, cliMCPBridgeOptions{
		name: "cursor-bin", enabled: p.toolExecutorConfigured,
		executor: p.cliToolBridgeState.wrappedExecutor(p.formatToolOutput),
	})
}
