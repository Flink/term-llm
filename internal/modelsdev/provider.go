package modelsdev

import (
	"net/url"
	"strings"
)

// ProviderForAPI resolves an exact published API authority and base path without
// cloning the catalog. Scheme and host are case-insensitive, trailing path
// slashes are ignored, and query strings, fragments and credentials are rejected.
// Missing or ambiguous endpoint metadata never guesses a provider.
func (c *Client) ProviderForAPI(endpoint string) (string, bool) {
	canonical := canonicalAPI(endpoint)
	if canonical == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var match string
	for key, provider := range c.catalog {
		if canonicalAPI(provider.API) != canonical {
			continue
		}
		if match != "" {
			return "", false
		}
		match = key
	}
	return match, match != ""
}

func canonicalAPI(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/")
}
