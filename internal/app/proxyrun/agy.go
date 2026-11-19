package proxyrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

var agyClient = clientSpec{"Antigravity", "agy", proxyclient.AgyNativeKind, proxyclient.ValidateAgyNativeToken, agyConfiguredConnection}

func agyConfiguredConnection(cfg *config.Config) (Connection, bool, error) {
	if cfg == nil {
		return Connection{}, false, nil
	}
	provider := cfg.GetProviderConfig(proxyclient.AgyNativeKind)
	if provider == nil || strings.TrimSpace(provider.ProxyBinding) == "" {
		return Connection{}, false, nil
	}
	binding, err := proxyclient.ResolveAgyNativeBinding(provider.ProxyBinding)
	return Connection{Root: binding.Root, Token: binding.Token}, true, err
}

// ResolveConfiguredAgyConnection returns the explicitly bound agy-bin route, if
// any. A configured but invalid binding is an error: it never falls back to a
// local Antigravity login.
func ResolveConfiguredAgyConnection(cfg *config.Config) (Connection, bool, error) {
	return agyConfiguredConnection(cfg)
}

// AgySharedOutput runs one non-interactive agy command as a shared worker in a
// fresh private HOME routed through the scoped proxy and returns its stdout.
// Native stderr is discarded because the child environment embeds the scoped
// token; failures never fall back to the installed agy login.
func AgySharedOutput(ctx context.Context, connection Connection, binary string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(connection.Root) == "" {
		return nil, errors.New("Antigravity proxy command requires a scoped proxy connection (no direct fallback)")
	}
	if err := proxyclient.ValidateAgyNativeToken(connection.Token); err != nil {
		return nil, err
	}
	stateDir, err := proxyclient.AgyNativeStateDir(connection.Root, connection.Token)
	if err != nil {
		return nil, err
	}
	proxy, err := proxyclient.PrepareAgyNativeProxy(ctx, connection.Root, connection.Token, stateDir)
	if err != nil {
		return nil, err
	}
	if !proxy.HasSharedIdentity() {
		return nil, errors.New("Antigravity proxy route has no shared sign-in yet; complete the operator sign-in first (no direct fallback)")
	}
	home, cleanup, err := prepareAgyLauncherHome(stateDir, proxy, false)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proxy, err = proxy.WithSystemTrust()
	if err != nil {
		return nil, err
	}
	env := proxyclient.AgyNativeEnvironment(os.Environ(), home, proxy)
	if err := proxyclient.CheckAgyNativeVersion(ctx, binary, home, env); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = env, home, childWaitDelay
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("agy through the Antigravity proxy was canceled or timed out: %w", ctxErr)
		}
		return nil, errors.New("agy failed through the Antigravity proxy; check agy version, shared operator sign-in and route grant")
	}
	return out, nil
}

// RunAgy launches native Antigravity with inherited stdin/stdout and a fresh,
// private Linux HOME for either shared use or operator enrollment.
func RunAgy(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	root, token, err := agyClient.resolve(opts)
	if err != nil {
		return err
	}
	stateDir, err := proxyclient.AgyNativeStateDir(root, token)
	if err != nil {
		return err
	}
	proxy, err := proxyclient.PrepareAgyNativeProxy(ctx, root, token, stateDir)
	if err != nil {
		return err
	}
	login := slices.Contains(opts.Args, "--login")
	home, cleanup, err := prepareAgyLauncherHome(stateDir, proxy, login)
	if err != nil {
		return err
	}
	defer cleanup()
	proxy, err = proxy.WithSystemTrust()
	if err != nil {
		return err
	}
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	env := proxyclient.AgyNativeEnvironment(opts.Env, home, proxy)
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		command = agyClient.executable
	}
	binary, err := exec.LookPath(command)
	if err != nil {
		return fmt.Errorf("locate agy: %w", err)
	}
	opts.Command, err = filepath.Abs(binary)
	if err != nil {
		return fmt.Errorf("resolve agy executable: %w", err)
	}
	if err := proxyclient.CheckAgyNativeVersion(ctx, opts.Command, home, env); err != nil {
		return err
	}
	if err := launch(ctx, opts, env, agyClient.executable, home); err != nil {
		return err
	}
	if !login {
		return nil
	}
	confirmed, err := proxyclient.PrepareAgyNativeProxy(ctx, root, token, stateDir)
	if err != nil {
		return fmt.Errorf("confirm Antigravity login with the proxy: %w", err)
	}
	if !confirmed.HasSharedIdentity() {
		return errors.New("Antigravity proxy has not confirmed login; retry `term-llm proxy run agy -- acp --login`")
	}
	return nil
}

func prepareAgyLauncherHome(stateDir string, proxy proxyclient.AgyNativeProxy, login bool) (string, func(), error) {
	shared := proxy.HasSharedIdentity()
	if shared && login {
		return "", nil, errors.New("Antigravity shared workers are already signed in by the proxy operator; omit --login")
	}
	if !shared && !login {
		return "", nil, errors.New("Antigravity proxy operator enrollment requires `term-llm proxy run agy -- acp --login`")
	}
	home, err := os.MkdirTemp(stateDir, "launcher-")
	if err != nil {
		return "", nil, errors.New("could not create the private Antigravity launcher home")
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	if shared {
		if err := proxy.SeedSharedIdentity(home); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return home, cleanup, nil
}
