package responsesproviders

import (
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

// A scoped OpenCode Go client uses one parent route with explicit protocol
// subroots: Chat/Responses at the base, Messages with x-api-key at /messages,
// and Gemini with x-goog-api-key at /gemini. Named providers keep their own
// namespace. Only explicitly registered legacy siblings remain compatible.
func openCodeGoProtocolRoots(baseURL string) (messages, gemini string, err error) {
	source, bound := proxyclient.Lookup(baseURL)
	if !bound {
		return baseURL, baseURL, nil
	}
	if !proxyclient.SameRoot(baseURL, source.Root) {
		return "", "", fmt.Errorf("OpenCode Go proxy API root is not explicitly registered; no direct fallback")
	}
	i := strings.LastIndex(source.Root, "/")
	if i < 0 {
		return "", "", fmt.Errorf("invalid OpenCode Go proxy route")
	}
	id := source.Root[i+1:]
	m, ok := proxyclient.Related(baseURL, id+"/messages")
	if !ok {
		m, ok = proxyclient.Related(baseURL, id+"-messages")
	}
	if !ok {
		return "", "", fmt.Errorf("OpenCode Go Messages proxy route is missing; no direct fallback")
	}
	g, ok := proxyclient.Related(baseURL, id+"/gemini")
	if !ok {
		g, ok = proxyclient.Related(baseURL, id+"-gemini")
	}
	if !ok {
		return "", "", fmt.Errorf("OpenCode Go Gemini proxy route is missing; no direct fallback")
	}
	return m.Root, g.Root, nil
}
