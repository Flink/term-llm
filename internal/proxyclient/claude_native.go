package proxyclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeNativeStateDir returns the private, per-binding state directory used
// for a proxy-bound native Claude installation. It contains only term-llm-owned
// state: the public proxy CA, the forced claude.ai login setting, and Claude's
// isolated session/login files. The "-v2" namespace prevents a profile created
// by the retired setup-token launcher from being reused. The scoped token is
// part of the directory name hash but is never written.
func ClaudeNativeStateDir(root, token string) (string, error) {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return "", errors.New("Claude proxy binding root is invalid")
	}
	if err := validateClaudeNativeToken(token); err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", errors.New("could not locate the private application cache directory")
	}
	sum := sha256.Sum256([]byte(canonical + "\x00" + token))
	return filepath.Join(cache, "term-llm", "claude-proxy-v2", hex.EncodeToString(sum[:16])), nil
}

// PrepareClaudeNativeEnvironment prepares the process-local environment for a
// native Claude client behind a Claude-only TLS-intercepting credential proxy.
// It fetches the proxy's public v2 CA and proxy origin from the binding root,
// stores the CA below stateDir with owner-only permissions, pins the isolated
// Claude profile to claude.ai subscription login, and returns env with
// competing vendor credentials, federation/profile state, cloud/auth-helper
// switches and ambient proxy settings removed. Unrelated entries are preserved.
//
// No vendor OAuth token is injected: the native client signs in normally
// through platform.claude.com and stores only the proxy's placeholder in the
// term-managed CLAUDE_CONFIG_DIR/ANTHROPIC_CONFIG_DIR. The returned environment
// trusts the proxy CA only for this process (through NODE_EXTRA_CA_CERTS); it
// never installs trust in the operating system and never falls back to a direct
// vendor credential. The caller owns stateDir and must keep it private.
func PrepareClaudeNativeEnvironment(ctx context.Context, root, token, stateDir string, env []string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("Claude proxy state directory is required")
	}
	bootstrap, err := fetchClaudeNativeBootstrap(ctx, root, token)
	if err != nil {
		return nil, err
	}
	caPath, err := prepareClaudeNativeState(stateDir, bootstrap.CAPEM)
	if err != nil {
		return nil, err
	}
	return claudeNativeEnvironment(env, bootstrap, token, caPath, stateDir)
}
