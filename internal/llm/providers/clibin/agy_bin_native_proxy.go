package clibin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/term-llm/term-llm/internal/cliwire"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

type agyBoundRoute struct {
	proxyclient.Binding
	dir string
}

func (p *AgyBinProvider) nativeProxyBinding() (*agyBoundRoute, error) {
	root := envValue(p.extraEnvList(), agyProxyRootEnv)
	if root == "" {
		return nil, nil
	}
	return resolveAgyBoundRoute(root)
}

func resolveAgyBoundRoute(root string) (*agyBoundRoute, error) {
	binding, err := proxyclient.ResolveAgyNativeBinding(root)
	if err != nil {
		return nil, err
	}
	dir, err := proxyclient.AgyNativeStateDir(binding.Root, binding.Token)
	if err != nil {
		return nil, err
	}
	return &agyBoundRoute{Binding: binding, dir: dir}, nil
}

// ReadAgyProxyUsage runs agy's read-only quota command using a registered
// agy-bin binding, with the same isolation as a bound inference worker.
func ReadAgyProxyUsage(ctx context.Context, binary, root string) ([]byte, error) {
	route, err := resolveAgyBoundRoute(root)
	if err != nil {
		return nil, err
	}
	worker := NewAgyBinProvider("", nil)
	worker.agyBinary = binary
	return worker.runBoundAgyCommand(ctx, route, "--output-format", "json", "-p=/usage")
}

// runBoundAgyCommand uses a scratch home instead of the generation session's
// home. The worker must be fresh and have its agy executable already resolved.
func (p *AgyBinProvider) runBoundAgyCommand(ctx context.Context, binding *agyBoundRoute, args ...string) (out []byte, retErr error) {
	if err := p.prepareHome(true, true); err != nil {
		return nil, err
	}
	defer os.RemoveAll(p.agyHome)
	defer p.cleanupRuntime()
	finish, err := p.startNativeProxyWorker(ctx, binding)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, finish()) }()
	cmd, err := p.newAgyCommand(ctx, args)
	if err != nil {
		return nil, err
	}
	cmd.Env = p.proxyEnv
	cmd.WaitDelay = 2 * time.Second
	// Do not surface child diagnostics that could contain proxy credentials.
	cmd.Stderr = io.Discard
	out, err = cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("agy through the Antigravity proxy was canceled or timed out: %w", ctx.Err())
		}
		return nil, fmt.Errorf("run agy through the Antigravity proxy: %w", err)
	}
	return out, nil
}

// agyHomeBase keeps bound sessions out of the direct agy-bin home cache and
// prevents resuming a worker home from a different root or scoped token.
func (p *AgyBinProvider) agyHomeBase() (string, error) {
	binding, err := p.nativeProxyBinding()
	if err != nil {
		return "", err
	}
	if binding == nil {
		return agyBinCacheBase()
	}
	return proxyclient.AgyNativeWorkerBase(binding.dir)
}

// startNativeProxyWorker admits a bound subprocess only after discovery
// supplies a shared identity. Alias-free operator enrollment is launcher-only.
// The caller must run the returned cleanup before discarding the worker HOME.
func (p *AgyBinProvider) startNativeProxyWorker(ctx context.Context, binding *agyBoundRoute) (func() error, error) {
	ambient := p.extraEnvList()
	if envValue(ambient, cliwire.TraceRootEnv) != "" {
		return nil, errors.New("wire audit cannot be chained with the scoped Antigravity proxy; unset " + cliwire.TraceRootEnv + " (no direct fallback)")
	}
	upstream, err := proxyclient.PrepareAgyNativeProxy(ctx, binding.Root, binding.Token, binding.dir)
	if err != nil {
		return nil, err
	}
	return p.installNativeProxyWorker(ctx, ambient, upstream)
}

func (p *AgyBinProvider) installNativeProxyWorker(ctx context.Context, ambient []string, upstream proxyclient.AgyNativeProxy) (func() error, error) {
	if !upstream.HasSharedIdentity() {
		return nil, errors.New("Antigravity proxy did not provide a shared worker identity; use a worker grant from the proxy operator (no direct fallback)")
	}
	if err := p.setupNativeProxyRuntime(ctx, ambient, upstream); err != nil {
		return nil, err
	}
	home := p.agyHome
	if err := upstream.SeedSharedIdentity(home); err != nil {
		return nil, err
	}
	return func() error { return clearAgyProxyWorkerIdentity(home) }, nil
}

func (p *AgyBinProvider) setupNativeProxyRuntime(ctx context.Context, ambient []string, upstream proxyclient.AgyNativeProxy) error {
	if err := clearAgyProxyWorkerIdentity(p.agyHome); err != nil {
		return err
	}
	p.isolation.SetUpstream(upstream.ProxyURL, upstream.CAFile)
	if err := p.isolation.EnsureStarted(p.agyHome); err != nil {
		return fmt.Errorf("start agy native-tool isolation: %w", err)
	}
	local := p.isolation.Environment()
	if local["HTTPS_PROXY"] == "" || local["SSL_CERT_FILE"] == "" {
		return errors.New("Antigravity rewrite proxy is not ready (no direct fallback)")
	}
	p.proxyEnv = proxyclient.AgyNativeEnvironment(ambient, p.agyHome, proxyclient.AgyNativeProxy{
		ProxyURL: local["HTTPS_PROXY"], CAFile: local["SSL_CERT_FILE"],
	})
	if runtime.GOOS == "darwin" {
		// agy selects file-based OAuth storage in SSH sessions. Do not
		// inherit real SSH metadata; supply only the local storage signal.
		p.proxyEnv = append(p.proxyEnv, "SSH_CLIENT=term-llm-private-home")
	}
	return proxyclient.CheckAgyNativeWorkerVersion(ctx, p.agyBinary, filepath.Join(p.agyHome, "cwd"), p.proxyEnv)
}

func clearAgyProxyWorkerIdentity(home string) error {
	if err := proxyclient.RemoveAgyWorkerIdentity(home); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear Antigravity worker identity: %w", err)
	}
	return nil
}

// newAgyCommand applies the mandatory worker sandbox only to bound launches.
// The direct provider keeps its separate login and rewrite proxy setup.
func (p *AgyBinProvider) newAgyCommand(ctx context.Context, args []string) (*exec.Cmd, error) {
	binary := p.agyBinary
	if p.proxyEnv != nil {
		binary, args = proxyclient.AgyNativeWorkerInvocation(binary, args)
	}
	return newCLICommand(ctx, binary, args, filepath.Join(p.agyHome, "cwd"))
}
