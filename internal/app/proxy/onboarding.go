package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

// startupOptions supplies CLI conveniences without weakening Open's explicit
// provisioning contract. Never infer an origin from forwarded/request headers.
func startupOptions(opts Options) (Options, error) {
	if opts.StateDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return opts, fmt.Errorf("locate private proxy state: %w (or set --state-dir)", err)
		}
		opts.StateDir = filepath.Join(dir, "term-llm", "proxy")
	}
	if opts.Auth == "" {
		opts.Auth = "passkey"
	}
	if opts.Auth != "passkey" {
		return opts, nil
	}
	local := loopbackHost(opts.Host)
	if opts.PublicURL == "" {
		if opts.Host != "localhost" && opts.Host != "127.0.0.1" && opts.Host != "::1" {
			return opts, fmt.Errorf("--public-url is required for this listener; set your browser's HTTPS origin (for example --public-url https://proxy.example.com), or use --host 127.0.0.1 for local setup")
		}
		opts.PublicURL = "http://" + net.JoinHostPort("localhost", strconv.Itoa(opts.Port))
	}
	u, err := url.Parse(opts.PublicURL)
	opts.autoBootstrap = local && err == nil && u.Scheme == "http" && u.Hostname() == "localhost"
	return opts, nil
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// The auth directory has already been checked and locked. A fresh file avoids
// symlink adoption and rearming a secret left behind by an interrupted process.
func createBootstrapFile(dir string) (path string, err error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", fmt.Errorf("generate enrollment secret: %w", err)
	}
	defer clear(secret[:])
	f, err := fileprivacy.CreateTemp(dir, "bootstrap-*")
	if err != nil {
		return "", fmt.Errorf("create enrollment secret file: %w", err)
	}
	path = f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = fmt.Fprintln(f, hex.EncodeToString(secret[:]))
	closeErr := f.Close()
	if err != nil {
		return path, fmt.Errorf("write enrollment secret: %w", err)
	}
	if closeErr != nil {
		return path, fmt.Errorf("close enrollment secret: %w", closeErr)
	}
	return path, nil
}

func (s *Service) printStartup(out io.Writer, address string) {
	adminURL := s.opts.PublicURL
	if adminURL == "" {
		adminURL = "http://" + address
	}
	fmt.Fprintf(out, "Proxy ready · %s\n", adminURL)
	if s.opts.DebugPayloadDir != "" {
		fmt.Fprintln(out, "Debug capture on · raw payloads may contain secrets.")
	}
	if s.passkey == nil || s.passkey.store.CredentialCount() != 0 {
		return
	}
	path := s.opts.PasskeyBootstrapTokenFile
	if s.passkey.generatedBootstrap != "" {
		path = s.passkey.generatedBootstrap
	}
	// POSIX shell quoting, including paths containing apostrophes or dollar signs.
	quoted := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
	fmt.Fprintf(out, "\nFirst passkey: paste this secret into Bootstrap token, then choose Create first passkey:\n  cat %s\nExpires in 10 minutes. Keep it private.\n", quoted)
	if s.passkey.generatedBootstrap != "" {
		fmt.Fprintln(out, "Restart for a new secret.")
	}
}
