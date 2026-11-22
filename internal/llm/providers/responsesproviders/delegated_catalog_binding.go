package responsesproviders

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyclient"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// delegatedCatalogBinding selects the exact registered root. A nested route
// that owns /models cannot borrow the parent route's credentials or cache.
func delegatedCatalogBinding(baseURL, kind string) (proxyclient.Binding, bool) {
	baseURL = strings.TrimSpace(baseURL)
	binding, ok := proxyclient.Lookup(baseURL)
	if !ok || binding.Kind != kind || !proxyclient.SameRoot(baseURL, binding.Root) {
		return proxyclient.Binding{}, false
	}
	operation, ok := proxyclient.Lookup(binding.Root + "/models")
	return binding, ok && operation.Root == binding.Root
}

// delegatedCatalogID preserves cache keys while ignoring attribution tags.
func delegatedCatalogID(binding proxyclient.Binding, endpoint string) string {
	caller, _ := proxyprofile.SplitCallerToken(binding.Token)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(endpoint+"\x00"+caller)))
}
