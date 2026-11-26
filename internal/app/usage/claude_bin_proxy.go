package usage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/term-llm/term-llm/internal/app/proxyrun"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

// configuredClaudeUsageProxy resolves only an explicitly configured claude-bin
// binding. The retired standalone connector receipt is never consulted: a
// missing binding means the established direct login path, while a configured
// but invalid binding fails closed with no local vendor fallback.
func configuredClaudeUsageProxy() (proxyrun.Connection, bool, error) {
	cfg, err := loadProviderUsageConfig()
	if err != nil {
		return proxyrun.Connection{}, false, errors.New("could not load Claude proxy usage configuration")
	}
	return configuredClaudeUsageProxyFor(cfg, "claude-bin")
}

func configuredClaudeUsageProxyFor(cfg *config.Config, providerName string) (proxyrun.Connection, bool, error) {
	connection, configured, err := proxyrun.ResolveConfiguredConnectionFor(cfg, providerName)
	if err != nil {
		return proxyrun.Connection{}, true, err
	}
	return connection, configured, nil
}

// fetchClaudeBinProxiedUsage sends get_usage through the same scoped tunnel as
// inference. A broken binding or launch is terminal: no installed vendor login
// may be used as a fallback.
func fetchClaudeBinProxiedUsage(ctx context.Context, binary string, connection proxyrun.Connection) (json.RawMessage, error) {
	if strings.TrimSpace(connection.Root) == "" || strings.TrimSpace(connection.Token) == "" {
		return nil, errors.New("Claude usage requires a scoped proxy connection (no direct fallback)")
	}
	stateDir, err := proxyclient.ClaudeNativeStateDir(connection.Root, connection.Token)
	if err != nil {
		return nil, err
	}
	cmd := newClaudeUsageCommand(ctx, binary)
	cleanup, err := proxyclient.PrepareClaudeNativeCommand(ctx, cmd, connection.Root, connection.Token, stateDir)
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	raw, err := runClaudeUsageCommand(ctx, cmd)
	if err != nil {
		return nil, claudeProxiedUsageError(ctx)
	}
	return raw, nil
}
