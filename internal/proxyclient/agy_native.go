package proxyclient

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/fileprivacy"
	"github.com/term-llm/term-llm/internal/mitmca"
)

const AgyNativeKind = "agy-bin"

var agyNativePolicy = nativePolicy{
	label: "Antigravity", discovery: agyNativeDiscoveryPath,
	hosts:           []string{"oauth2.googleapis.com", "www.googleapis.com", "daily-cloudcode-pa.googleapis.com"},
	hostDescription: "oauth2.googleapis.com, www.googleapis.com and daily-cloudcode-pa.googleapis.com",
	validateToken:   ValidateAgyNativeToken,
}

// ValidateAgyNativeToken requires a proxy-issued scoped client token, not a
// Google credential, a vault placeholder or a proxy administrator key.
func ValidateAgyNativeToken(token string) error {
	if !strings.HasPrefix(token, "sk-tlp-") || token == "sk-tlp-" {
		return errors.New("Antigravity requires a scoped proxy client token (sk-tlp-…)")
	}
	return validateNativeToken("Antigravity", token, "ya29.")
}

// ResolveAgyNativeBinding requires the exact registered root, not a subpath
// of an existing grant or a binding for another client.
func ResolveAgyNativeBinding(root string) (Binding, error) {
	binding, ok := Lookup(root)
	if !ok || binding.Kind != AgyNativeKind || !SameRoot(root, binding.Root) {
		return Binding{}, errors.New("agy-bin proxy binding is not a registered scoped agy-bin route (no direct fallback)")
	}
	if err := ValidateAgyNativeToken(binding.Token); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

// AgyNativeStateDir is shared by the native launcher and worker for one
// exact root and scoped caller. It never points at the installed agy home.
func AgyNativeStateDir(root, token string) (string, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", errors.New("Antigravity proxy workers are only supported on Linux and macOS (no direct fallback)")
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return "", agyNativePolicy.errorf("binding root is invalid")
	}
	u, _ := url.Parse(canonical)
	if u.Scheme != "https" && !agyNativeLoopback(u.Hostname()) {
		return "", agyNativePolicy.errorf("requires HTTPS for non-loopback bindings")
	}
	dir, err := agyNativePolicy.stateDir("agy-proxy-v2", root, token)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("Antigravity proxy requires an absolute private cache directory")
	}
	return dir, nil
}

func agyNativeLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}

// AgyNativeProxy is the validated native CONNECT endpoint and process-local
// trust anchor. ProxyURL includes scoped Basic credentials; never log it.
type AgyNativeProxy struct {
	ProxyURL string
	CAFile   string
	identity agySharedIdentity
}

func PrepareAgyNativeProxy(ctx context.Context, root, token, stateDir string) (AgyNativeProxy, error) {
	expected, err := AgyNativeStateDir(root, token)
	if err != nil {
		return AgyNativeProxy{}, err
	}
	if stateDir != expected {
		return AgyNativeProxy{}, errors.New("Antigravity proxy state does not match the scoped binding")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bootstrap, err := agyNativePolicy.fetchBootstrap(ctx, root, token)
	if err != nil {
		return AgyNativeProxy{}, err
	}
	// Refuse a symlink at the provider-owned namespace, not just at the leaf.
	if err := ensureAgyState(stateDir); err != nil {
		return AgyNativeProxy{}, err
	}
	caPath, err := agyNativePolicy.prepareCA(stateDir, "agy-proxy-ca-v2.pem", bootstrap.CAPEM)
	if err != nil {
		return AgyNativeProxy{}, err
	}
	return AgyNativeProxy{ProxyURL: bootstrap.connectURL(token), CAFile: caPath, identity: bootstrap.agyIdentity}, nil
}

// HasSharedIdentity reports whether the proxy supplied a worker identity.
// Without it, the binding uses the operator's isolated native login flow.
func (p AgyNativeProxy) HasSharedIdentity() bool { return p.identity.access != "" }

// SeedSharedIdentity installs only the scoped aliases and the native OAuth
// envelope into a private home, without reading any existing login profile.
func (p AgyNativeProxy) SeedSharedIdentity(home string) error {
	return p.identity.seed(home, time.Now())
}

// WithSystemTrust prepares a separate launcher bundle for both the public
// HTTPS CONNECT origin and the constrained upstream interception CA. Workers
// pass the original CAFile to the local rewrite proxy instead.
func (p AgyNativeProxy) WithSystemTrust() (AgyNativeProxy, error) {
	ca, err := os.ReadFile(p.CAFile)
	if err != nil {
		return AgyNativeProxy{}, agyNativePolicy.errorf("could not read the launcher CA")
	}
	bundlePath := filepath.Join(filepath.Dir(p.CAFile), "agy-launcher-trust.pem")
	if err := fileprivacy.WriteFileAtomic(bundlePath, mitmca.SystemRootsBundle(ca)); err != nil {
		return AgyNativeProxy{}, agyNativePolicy.errorf("could not write the launcher trust bundle")
	}
	p.CAFile = bundlePath
	return p, nil
}

// AgyNativeWorkerBase is a private namespace for resumable worker homes.
func AgyNativeWorkerBase(stateDir string) (string, error) {
	if err := ensureAgyState(stateDir); err != nil {
		return "", err
	}
	return ensureAgyPrivateDir(filepath.Join(stateDir, "workers"))
}

func ensureAgyState(stateDir string) error {
	for _, dir := range []string{filepath.Dir(stateDir), stateDir} {
		if _, err := ensureAgyPrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func ensureAgyPrivateDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil || checkNativePrivateDir(path) != nil {
		return "", errors.New("Antigravity proxy requires a private non-symlink directory")
	}
	return path, nil
}

// AgyNativeEnvironment keeps only basic terminal/process settings, redirects
// XDG lookup to the private HOME, and omits the session bus, desktop/browser
// environment, inherited proxy exclusions and Google credential overrides.
// It configures the child, not an OS sandbox or a system/browser proxy.
func AgyNativeEnvironment(env []string, home string, proxy AgyNativeProxy) []string {
	out := make([]string, 0, 24)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "PATH", "TERM", "COLORTERM", "LANG", "LANGUAGE", "TZ", "NO_COLOR":
			out = append(out, entry)
		default:
			if strings.HasPrefix(key, "LC_") {
				out = append(out, entry)
			}
		}
	}
	return append(out,
		"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"DBUS_SESSION_BUS_ADDRESS=", "XDG_RUNTIME_DIR="+filepath.Join(home, "runtime"),
		"BROWSER=/bin/false",
		"HTTPS_PROXY="+proxy.ProxyURL, "https_proxy="+proxy.ProxyURL,
		"HTTP_PROXY="+proxy.ProxyURL, "http_proxy="+proxy.ProxyURL,
		"NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1",
		"SSL_CERT_FILE="+proxy.CAFile, "NODE_EXTRA_CA_CERTS="+proxy.CAFile,
		// Do not set SSL_CERT_DIR: an inherited override was already discarded,
		// and the native client must retain the default system directories.
	)
}
