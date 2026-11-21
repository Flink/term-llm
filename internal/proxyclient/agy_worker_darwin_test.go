package proxyclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Re-exec only this test inside the worker sandbox so the test can distinguish
// an OS-level denial from a connection failure. UDP Dial sends no datagrams.
func TestAgyWorkerSandbox(t *testing.T) {
	if os.Getenv("TERM_LLM_TEST_AGY_SANDBOX_CHILD") == "1" {
		if conn, err := net.Dial("udp4", "192.0.2.1:443"); !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
			if conn != nil {
				conn.Close()
			}
			t.Fatalf("off-loopback connection was not denied by the sandbox: %v", err)
		}
		if _, err := os.ReadFile(os.Getenv("TERM_LLM_TEST_AGY_KEYCHAIN")); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("Keychain read was not denied: %v", err)
		}
		if err := exec.Command("/usr/bin/security", "help").Start(); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("execution of security was not denied: %v", err)
		}
		res, err := http.Get(os.Getenv("TERM_LLM_TEST_AGY_LOOPBACK"))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("sandboxed loopback status: %s", res.Status)
		}
		return
	}

	keychainDir := filepath.Join(t.TempDir(), "Keychains")
	if err := os.Mkdir(keychainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(keychainDir, "login.keychain-db")
	if err := os.WriteFile(canary, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command, args := AgyNativeWorkerInvocation(binary, []string{"-test.run=^TestAgyWorkerSandbox$"})
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), "TERM_LLM_TEST_AGY_SANDBOX_CHILD=1", "TERM_LLM_TEST_AGY_KEYCHAIN="+canary, "TERM_LLM_TEST_AGY_LOOPBACK="+srv.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worker sandbox: %v\n%s", err, out)
	}
}
