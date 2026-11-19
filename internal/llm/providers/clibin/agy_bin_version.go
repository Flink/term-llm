package clibin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

// agyBinMinimumVersion is the first agy release that honours the MCP
// antigravity/alwaysLoad tool annotation. agy-bin requires eager bridge schemas
// and native names for proxy isolation; older custom agents also failed to call
// lazily loaded MCP tools (agy issue #674).
const agyBinMinimumVersion = "1.3.3"

const agyVersionCheckTimeout = 10 * time.Second

// AgyVersionAtLeast reports whether agy names a release at or above the requested version.
func AgyVersionAtLeast(version string, major, minor, patch int) bool {
	return proxyclient.AgyVersionAtLeast(version, major, minor, patch)
}

// agyVersionOutput runs `agy --version` with the provider's configured
// environment; tests replace it.
var agyVersionOutput = func(ctx context.Context, binary string, env []string) ([]byte, error) {
	cmd, err := newCLICommand(ctx, binary, []string{"--version"}, "")
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd.Output()
}

// ensureSupportedVersion checks the resolved agy binary once per path.
func (p *AgyBinProvider) ensureSupportedVersion(ctx context.Context) error {
	if p.versionCheckedBinary == p.agyBinary {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, agyVersionCheckTimeout)
	defer cancel()
	out, err := agyVersionOutput(ctx, p.agyBinary, p.extraEnvList())
	if err != nil {
		return fmt.Errorf("check agy version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	if !AgyVersionAtLeast(version, 1, 3, 3) {
		if version == "" {
			version = "unknown"
		}
		return &UserFacingProviderError{
			Summary: fmt.Sprintf("agy-bin requires agy %s or newer (found %s)", agyBinMinimumVersion, version),
			Detail:  "Run `agy update` to upgrade the Antigravity CLI.",
		}
	}
	p.versionCheckedBinary = p.agyBinary
	return nil
}
