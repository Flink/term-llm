package proxyrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

var errAgyLauncherPlatform = errors.New("the standalone Antigravity proxy launcher requires Linux; on macOS use the bound agy-bin provider (no direct fallback)")

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
	if runtime.GOOS != "linux" {
		return errAgyLauncherPlatform
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
