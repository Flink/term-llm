package usage

import (
	"context"
	"errors"
	"strings"

	"github.com/term-llm/term-llm/internal/app/proxyrun"
)

// agyUsageProxiedCommand runs agy's read-only /usage print command as a shared
// worker through the configured scoped route. It is a seam for tests.
var agyUsageProxiedCommand = func(ctx context.Context, binary string, connection proxyrun.Connection) ([]byte, error) {
	return proxyrun.AgySharedOutput(ctx, connection, binary, "--output-format", "json", "-p=/usage")
}

// configuredAgyUsageProxy resolves only an explicitly configured agy-bin
// binding. A configured but invalid binding fails closed with no local
// Antigravity login fallback.
func configuredAgyUsageProxy() (proxyrun.Connection, bool, error) {
	cfg, err := loadProviderUsageConfig()
	if err != nil {
		return proxyrun.Connection{}, false, errors.New("could not load Antigravity proxy usage configuration")
	}
	return proxyrun.ResolveConfiguredAgyConnection(cfg)
}

// redactAgyProxyToken keeps the scoped token out of agy-provided response text.
func redactAgyProxyToken(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}
