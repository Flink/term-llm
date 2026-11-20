package proxyrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const childWaitDelay = 2 * time.Second

// Connection is one exact scoped proxy binding root. It never holds a vendor
// credential: the token is a proxy client token used only for metadata and
// Proxy-Authorization.
type Connection struct {
	Root  string
	Token string
}

// Options configures one native client launch. Injectable fields exist for
// focused tests; production callers use the terminal, environment and config.
type Options struct {
	URL, Token, Command string
	Args                []string
	In                  io.Reader
	Out, Err            io.Writer
	Env                 []string
	LoadConfig          func() (*config.Config, error)
	Prepare             func(context.Context, *exec.Cmd, string, string, string) (func(), error)
}

// ChildExitError reports that the native client ran and exited non-zero. The
// command adapter translates it to the process exit code without reprinting the
// child's own diagnostics.
type ChildExitError struct {
	Code   int
	client string
}

func (e *ChildExitError) Error() string {
	return fmt.Sprintf("%s exited with status %d", e.client, e.Code)
}

type clientSpec struct {
	label, executable, provider string
	validateToken               func(string) error
	configuredConnection        func(*config.Config) (Connection, bool, error)
}

var claudeClient = clientSpec{"Claude", "claude", "claude-bin", proxyclient.ValidateClaudeNativeToken, ResolveConfiguredConnection}

// ResolveConfiguredConnection returns the explicitly bound claude-bin route, if
// any. A configured but invalid binding is an error: it never falls back to a
// local Claude login or vendor credential.
func ResolveConfiguredConnection(cfg *config.Config) (Connection, bool, error) {
	if cfg == nil {
		return Connection{}, false, nil
	}
	provider := cfg.GetProviderConfig("claude-bin")
	if provider == nil || strings.TrimSpace(provider.ProxyBinding) == "" {
		return Connection{}, false, nil
	}
	binding, ok := proxyclient.Lookup(provider.ProxyBinding)
	if !ok || binding.Kind != "claude-bin" || !proxyclient.SameRoot(provider.ProxyBinding, binding.Root) {
		return Connection{}, true, errors.New("claude-bin proxy binding is not a registered scoped claude-bin route (no direct fallback)")
	}
	if err := proxyclient.ValidateClaudeNativeToken(binding.Token); err != nil {
		return Connection{}, true, errors.New("claude-bin proxy binding does not hold a scoped proxy client token (no direct fallback)")
	}
	return Connection{Root: binding.Root, Token: binding.Token}, true, nil
}

// Run prepares a process-local native Claude environment and launches the
// client with inherited stdio. An explicit URL/token pair bypasses
// configuration; otherwise the configured claude-bin binding is required.
// Preparation failures happen before exec, so a failed proxy binding can never
// silently use the installed Claude login.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	root, token, err := claudeClient.resolve(opts)
	if err != nil {
		return err
	}
	stateDir, err := proxyclient.ClaudeNativeStateDir(root, token)
	if err != nil {
		return err
	}
	prepare := opts.Prepare
	if prepare == nil {
		prepare = proxyclient.PrepareClaudeNativeCommand
	}
	cmd := nativeCommand(ctx, opts, opts.Env, claudeClient.executable, "")
	cleanup, err := prepare(ctx, cmd, root, token, stateDir)
	if err != nil {
		return fmt.Errorf("prepare Claude proxy environment: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return runNativeCommand(ctx, cmd, claudeClient.executable)
}

func (s clientSpec) resolve(opts Options) (string, string, error) {
	root, token := strings.TrimSpace(opts.URL), opts.Token
	if root != "" || token != "" {
		if root == "" || token == "" {
			return "", "", fmt.Errorf("%s proxy run requires both --url and --token, or neither", s.label)
		}
		resolvedRoot, err := config.ResolveProxySRV(root)
		if err != nil {
			return "", "", fmt.Errorf("resolve %s proxy URL: %w", s.label, err)
		}
		resolved, err := config.Cred(token).Resolve()
		if err != nil || resolved == "" {
			return "", "", fmt.Errorf("%s proxy token could not be resolved; check the credential reference", s.label)
		}
		if err := s.validateToken(resolved); err != nil {
			return "", "", fmt.Errorf("%s proxy token must be a scoped proxy client token, not a vendor credential", s.label)
		}
		return resolvedRoot, resolved, nil
	}
	load := opts.LoadConfig
	if load == nil {
		load = config.Load
	}
	cfg, err := load()
	if err != nil {
		return "", "", fmt.Errorf("load config for %s proxy run: %w", s.label, err)
	}
	connection, configured, err := s.configuredConnection(cfg)
	if err != nil {
		return "", "", err
	}
	if !configured {
		return "", "", fmt.Errorf("%s proxy run requires --url and --token or a configured %s proxy binding; direct native login is not used", s.label, s.provider)
	}
	return connection.Root, connection.Token, nil
}

func launch(ctx context.Context, opts Options, env []string, client, dir string) error {
	return runNativeCommand(ctx, nativeCommand(ctx, opts, env, client, dir), client)
}

func nativeCommand(ctx context.Context, opts Options, env []string, client, dir string) *exec.Cmd {
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		command = client
	}
	in, out, errOut := opts.In, opts.Out, opts.Err
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	cmd := exec.CommandContext(ctx, command, opts.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	cmd.Env, cmd.Dir = env, dir
	cmd.WaitDelay = childWaitDelay
	return cmd
}

func runNativeCommand(ctx context.Context, cmd *exec.Cmd, client string) error {
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code < 0 {
			code = 130
		}
		return &ChildExitError{Code: code, client: client}
	}
	if ctx.Err() != nil {
		return &ChildExitError{Code: 130, client: client}
	}
	return fmt.Errorf("run %s: %w", client, err)
}
