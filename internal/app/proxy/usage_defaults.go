package proxy

import (
	"net/url"
	"strings"
)

const (
	veniceOrigin   = "https://api.venice.ai:443"
	veniceRootPath = "/api/v1"
)

// isCanonicalVeniceRoot recognizes the public Venice API root, not display
// names, custom OpenAI-compatible upstreams, or other paths on the same host.
func isCanonicalVeniceRoot(raw string) bool {
	origin, err := Origin(raw)
	if err != nil || origin != veniceOrigin {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && !u.ForceQuery && strings.TrimSuffix(u.EscapedPath(), "/") == veniceRootPath
}

// defaultRouteUsage covers legacy Venice routes created before presets enabled
// observation. An explicit usage: off is never overridden.
func defaultRouteUsage(r *Route) {
	if r.Usage == "" && isCanonicalVeniceRoot(r.Upstream) {
		r.Usage = "auto"
	}
}
