package usage

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/term-llm/term-llm/internal/app/proxyrun"
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
	connection, configured, err := proxyrun.ResolveConfiguredConnection(cfg)
	if err != nil {
		return proxyrun.Connection{}, true, err
	}
	return connection, configured, nil
}

// prepareClaudeUsageEnvironment prepares a process-local native Claude
// environment for the same explicit proxy connection. Live limits come from
// the native get_usage control request through the Claude-only TLS tunnel; the
// scoped token headers used for the control connection are produced by the
// shared client helper. A preparation failure is terminal and never falls back
// to a worker's installed vendor login.
func prepareClaudeUsageEnvironment(ctx context.Context, connection proxyrun.Connection) ([]string, error) {
	if strings.TrimSpace(connection.Root) == "" || strings.TrimSpace(connection.Token) == "" {
		return nil, errors.New("Claude usage requires a scoped proxy connection (no direct fallback)")
	}
	stateDir, err := proxyclient.ClaudeNativeStateDir(connection.Root, connection.Token)
	if err != nil {
		return nil, err
	}
	return proxyclient.PrepareClaudeNativeEnvironment(ctx, connection.Root, connection.Token, stateDir, os.Environ())
}
