package proxyclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ClaudeNativeStateDir returns the private, per-binding state directory used
// for a proxy-bound native Claude installation. It contains only term-llm-owned
// state: the public proxy CA, the forced claude.ai login setting, and Claude's
// isolated session/login files. The "-v2" namespace prevents a profile created
// by the retired setup-token launcher from being reused. The scoped token
// secret (without any caller tag) is hashed for the directory name but
// never written.
func ClaudeNativeStateDir(root, token string) (string, error) {
	return claudeNativePolicy.stateDir("claude-proxy-v2", root, token)
}

func (p nativePolicy) stateDir(namespace, root, token string) (string, error) {
	canonical, err := canonicalRoot(root)
	if err != nil || !safeNativeRootPath(canonical) {
		return "", p.errorf("binding root is invalid")
	}
	if err := p.validateToken(token); err != nil {
		return "", err
	}
	// Honor XDG_CACHE_HOME on every OS; os.UserCacheDir ignores it on macOS
	// and Windows.
	cache := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if cache == "" {
		var err error
		if cache, err = os.UserCacheDir(); err != nil {
			return "", errors.New("could not locate the private application cache directory")
		}
	}
	sum := sha256.Sum256([]byte(canonical + "\x00" + callerSecret(token)))
	return filepath.Join(cache, "term-llm", namespace, hex.EncodeToString(sum[:16])), nil
}

// PrepareClaudeNativeEnvironment prepares an environment for the v2 operator
// login flow. New launches should use PrepareClaudeNativeCommand so they also
// support shared-worker metadata that requires a credential descriptor.
func PrepareClaudeNativeEnvironment(ctx context.Context, root, token, stateDir string, env []string) ([]string, error) {
	bootstrap, err := fetchClaudeBootstrap(ctx, root, token, stateDir)
	if err != nil {
		return nil, err
	}
	if bootstrap.claudeAccess.placeholder != "" {
		return nil, errors.New("Claude proxy shared access requires command-aware preparation")
	}
	return prepareClaudeNativeEnv(env, bootstrap, token, stateDir)
}

// PrepareClaudeNativeCommand replaces the child environment with scoped proxy
// routing, a process-local CA, and per-binding Claude configuration. Shared
// worker metadata also supplies a scoped access placeholder; neither a vendor
// OAuth token nor a refresh token is installed. If cleanup is non-nil, the
// caller must invoke it after the launch attempt (including on failure).
func PrepareClaudeNativeCommand(ctx context.Context, cmd *exec.Cmd, root, token, stateDir string) (cleanup func(), err error) {
	bootstrap, err := fetchClaudeBootstrap(ctx, root, token, stateDir)
	if err != nil {
		return nil, err
	}
	return prepareClaudeNativeCommand(cmd, bootstrap, token, stateDir)
}

func fetchClaudeBootstrap(ctx context.Context, root, token, stateDir string) (*nativeBootstrap, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("Claude proxy state directory is required")
	}
	return claudeNativePolicy.fetchBootstrap(ctx, root, token)
}

func prepareClaudeNativeCommand(cmd *exec.Cmd, bootstrap *nativeBootstrap, token, stateDir string) (func(), error) {
	env, err := prepareClaudeNativeEnv(cmd.Environ(), bootstrap, token, stateDir)
	if err != nil {
		return nil, err
	}
	if bootstrap.claudeAccess.placeholder == "" {
		if err := clearClaudeNativeLogin(stateDir); err != nil {
			return nil, err
		}
	}
	cmd.Env = env
	return bootstrap.claudeAccess.apply(cmd)
}

// claudeNativeCredentialsFile is where Claude keeps its file-based login
// inside CLAUDE_CONFIG_DIR (macOS uses the keychain instead).
const claudeNativeCredentialsFile = ".credentials.json"

// The proxy returns classic bootstrap only while an operator sign-in is armed
// for this client. Its isolated profile may still hold an earlier proxy-issued
// login alias (never a vendor token); remove it so Claude starts the sign-in
// instead of reusing that login and leaving the enrollment unused.
func clearClaudeNativeLogin(stateDir string) error {
	err := os.Remove(filepath.Join(filepath.Clean(stateDir), claudeNativeCredentialsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("Claude proxy could not clear the previous login for operator sign-in")
	}
	return nil
}

func prepareClaudeNativeEnv(env []string, bootstrap *nativeBootstrap, token, stateDir string) ([]string, error) {
	caPath, err := prepareClaudeNativeState(stateDir, bootstrap.CAPEM)
	if err != nil {
		return nil, err
	}
	return claudeNativeEnvironment(env, bootstrap, token, caPath, stateDir)
}
