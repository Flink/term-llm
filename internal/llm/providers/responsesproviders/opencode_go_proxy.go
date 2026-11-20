package responsesproviders

import (
	"fmt"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

// A scoped OpenCode Go client uses one parent route with explicit protocol
// subroots: Chat/Responses at the base, Messages with x-api-key at /messages,
// and Gemini with x-goog-api-key at /gemini. Look up both by their registered
// protocol profiles so a named provider cannot borrow another route.
func openCodeGoProtocolRoots(baseURL string) (messages, gemini string, err error) {
	source, bound := proxyclient.Lookup(baseURL)
	if !bound {
		return baseURL, baseURL, nil
	}
	if !proxyclient.SameRoot(baseURL, source.Root) {
		return "", "", fmt.Errorf("OpenCode Go proxy API root is not explicitly registered; no direct fallback")
	}
	m, ok := proxyclient.Related(baseURL, "opencode-go-messages")
	if !ok {
		return "", "", fmt.Errorf("OpenCode Go Messages proxy route is missing; no direct fallback")
	}
	g, ok := proxyclient.Related(baseURL, "opencode-go-gemini")
	if !ok {
		return "", "", fmt.Errorf("OpenCode Go Gemini proxy route is missing; no direct fallback")
	}
	return m.Root, g.Root, nil
}
