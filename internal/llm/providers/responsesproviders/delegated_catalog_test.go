package responsesproviders

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// Registered proxy routes live for the lifetime of the process. Keep them
// away from tests of direct account catalogs, even with shuffle or repeats.
func runProxyCatalogTestInChild(t *testing.T) bool {
	t.Helper()
	const child = "TERM_LLM_TEST_PROXY_CATALOG_CHILD"
	if os.Getenv(child) == t.Name() {
		return true
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Env = append(os.Environ(), child+"="+t.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proxy catalog test: %v\n%s", err, out)
	}
	return false
}
