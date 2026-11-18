package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

func TestStartupOptions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, host, origin, want string
		auto, fail               bool
	}{
		{name: "local", host: "127.0.0.1", want: "http://localhost:8123", auto: true},
		{name: "IPv6", host: "::1", want: "http://localhost:8123", auto: true},
		{name: "localhost", host: "localhost", want: "http://localhost:8123", auto: true},
		{name: "nonstandard loopback", host: "127.0.0.2", fail: true},
		{name: "wildcard", host: "0.0.0.0", fail: true},
		{name: "IPv6 wildcard", host: "::", fail: true},
		{name: "remote", host: "192.0.2.1", fail: true},
		{name: "remote explicit", host: "0.0.0.0", origin: passkeyOrigin, want: passkeyOrigin},
		{name: "remote local origin", host: "0.0.0.0", origin: "http://localhost:8123", want: "http://localhost:8123"},
		{name: "reverse proxy", host: "127.0.0.1", origin: passkeyOrigin, want: passkeyOrigin},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, err := startupOptions(Options{Host: test.host, Port: 8123, PublicURL: test.origin})
			if test.fail {
				if err == nil || !strings.Contains(err.Error(), "--public-url") {
					t.Fatalf("expected actionable public URL error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.PublicURL != test.want || opts.autoBootstrap != test.auto || opts.StateDir != filepath.Join(configDir, "term-llm", "proxy") {
				t.Fatalf("unexpected defaults: %+v", opts)
			}
		})
	}
}

func TestAutomaticLocalBootstrapLifecycle(t *testing.T) {
	opts, err := startupOptions(Options{Host: "127.0.0.1", Port: 8082, StateDir: filepath.Join(t.TempDir(), "private state's directory")})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := s.passkey.generatedBootstrap
	secret, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(secret)))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("bootstrap must have 256 random bits: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileprivacy.CheckFile(info); err != nil {
		t.Fatal(err)
	}
	// The generated secret uses the same one-use exchange as operator files.
	token, err := s.passkey.bootstrap.Verify(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.passkey.bootstrap.Authenticate(token.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.passkey.bootstrap.Verify(secret); err == nil {
		t.Fatal("bootstrap secret reused")
	}
	var out bytes.Buffer
	s.printStartup(&out, "127.0.0.1:8082")
	text := out.String()
	for _, want := range []string{"Proxy ready · http://localhost:8082\n", strings.ReplaceAll(path, "'", "'\"'\"'"), "Create first passkey", "cat '", "10 minutes", "Restart", "'\"'\"'"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in startup instructions: %s", want, text)
		}
	}
	if strings.Contains(text, strings.TrimSpace(string(secret))) {
		t.Fatal("secret printed to startup output")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary bootstrap file retained: %v", err)
	}
	restarted, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	fresh, err := os.ReadFile(restarted.passkey.generatedBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(secret, fresh) {
		t.Fatal("restart rearmed old enrollment secret")
	}
}

func TestAutomaticBootstrapDoesNotReplaceOperatorProvisioning(t *testing.T) {
	for _, test := range []struct{ name, host, origin string }{
		{"remote listener", "0.0.0.0", passkeyOrigin},
		{"remote origin", "127.0.0.1", passkeyOrigin},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, err := startupOptions(Options{Host: test.host, Port: 8082, PublicURL: test.origin, StateDir: filepath.Join(t.TempDir(), "state")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(opts); err == nil || !strings.Contains(err.Error(), "--passkey-bootstrap-token-file") {
				t.Fatalf("remote setup must require explicit bootstrap: %v", err)
			}
		})
	}
	s, opts := passkeyFixture(t)
	bootstrapPasskey(t, newPasskeyBrowser(t, s))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opts.PasskeyBootstrapTokenFile = ""
	opts, err := startupOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.passkey.generatedBootstrap != "" || restarted.passkey.bootstrap.Enabled() {
		t.Fatal("enrolled proxy reenabled bootstrap")
	}
	var out bytes.Buffer
	restarted.printStartup(&out, "127.0.0.1:8082")
	if out.String() != "Proxy ready · "+opts.PublicURL+"\n" {
		t.Fatal(out.String())
	}
}

func TestRunLocalOnboarding(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := Run(ctx, Options{Port: 0}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "http://localhost:") || strings.Contains(out.String(), "localhost:0/") {
		t.Fatal(out.String())
	}
	configDir, _ := os.UserConfigDir()
	files, err := filepath.Glob(filepath.Join(configDir, "term-llm", "proxy", "admin-auth", "bootstrap-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("bootstrap files after shutdown: %v, %v", files, err)
	}
}

func TestLocalEnrollmentSurvivesRestartWithoutNewBootstrap(t *testing.T) {
	opts, err := startupOptions(Options{Host: "127.0.0.1", Port: 8082, StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, credential := localAssertionCredential(t)
	if _, err := s.passkey.store.CommitFirstCredential(credential, "My laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.passkey.generatedBootstrap != "" || restarted.passkey.bootstrap.Enabled() {
		t.Fatal("enrolled local startup generated another bootstrap secret")
	}
	if restarted.passkey.store.CredentialCount() != 1 {
		t.Fatal("enrollment lost")
	}
}

func TestBootstrapFileCleanupOnStartupFailure(t *testing.T) {
	opts, err := startupOptions(Options{Host: "127.0.0.1", Port: 8082, StateDir: filepath.Join(t.TempDir(), "state"), ConfigFile: "missing-config.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Open(opts); err == nil {
		s.Close()
		t.Fatal("expected configuration error")
	}
	files, err := filepath.Glob(filepath.Join(opts.StateDir, "admin-auth", "bootstrap-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("secret files after failed startup: %v, %v", files, err)
	}
	opts.ConfigFile = ""
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("failed startup retained auth lock: %v", err)
	}
	defer s.Close()
}

func TestLocalExplicitBootstrapFileRemainsOperatorOwned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operator-bootstrap")
	if err := os.WriteFile(path, []byte(hostSecret), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := startupOptions(Options{Host: "127.0.0.1", Port: 8082, StateDir: filepath.Join(dir, "state"), PasskeyBootstrapTokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.passkey.generatedBootstrap != "" {
		t.Fatal("explicit secret replaced")
	}
	if _, err := s.passkey.bootstrap.Verify([]byte(hostSecret)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != hostSecret {
		t.Fatalf("operator secret changed: %v", err)
	}
}

func TestProxyStartupIsConcise(t *testing.T) {
	for _, test := range []struct {
		name  string
		debug bool
		want  string
	}{
		{"normal", false, "Proxy ready · http://localhost:8082\n"},
		{"capture", true, "Proxy ready · http://localhost:8082\nDebug capture on · raw payloads may contain secrets.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testService(t)
			s.opts.PublicURL = "http://localhost:8082"
			if test.debug {
				s.opts.DebugPayloadDir = filepath.Join(t.TempDir(), strings.Repeat("long-path-", 30))
			}
			var out bytes.Buffer
			s.printStartup(&out, "127.0.0.1:8082")
			if out.String() != test.want {
				t.Fatalf("startup output: %q", out.String())
			}
		})
	}
}
