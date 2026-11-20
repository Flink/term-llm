package proxy

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/term-llm/term-llm/internal/app/proxy/grantscope"
	"golang.org/x/net/http/httpguts"
)

// compileRoute keeps one public parent and one authority for all destinations.
// Selected copies inherit the parent's identity, accounting and lifecycle policy.
func compileRoute(r *Route, secrets map[string]material, discovery *upstreamDiscovery) (activeRoute, error) {
	defaultRouteUsage(r)
	if err := validatePolicy(r); err != nil {
		return activeRoute{}, err
	}
	if r.RequestIDHeader != "" && !validHeader(r.RequestIDHeader) {
		return activeRoute{}, fmt.Errorf("invalid request ID header")
	}
	if len(r.Rules) == 0 {
		return compileDestination(r, secrets, discovery)
	}
	if len(r.Rules) > 32 {
		return activeRoute{}, fmt.Errorf("at most 32 subpath rules per route")
	}
	if r.Upstream != "" || len(r.SetHeaders) != 0 || len(r.Allow) != 0 || r.CallerAuth != (Auth{}) {
		return activeRoute{}, fmt.Errorf("rules cannot be combined with parent upstream, caller_auth, set_headers or allow")
	}
	parent := activeRoute{}
	seen := map[string]bool{}
	credentials := map[string]bool{}
	// Copy before normalizing so compilation never rewrites a caller's rules.
	r.Rules = append([]RouteRule(nil), r.Rules...)
	for i := range r.Rules {
		rule := &r.Rules[i]
		if !canonicalPolicyPath(rule.PathPrefix) || !strings.HasSuffix(rule.PathPrefix, "/") || seen[rule.PathPrefix] {
			return activeRoute{}, fmt.Errorf("rule path_prefix must be a unique canonical slash-rooted prefix ending in /")
		}
		seen[rule.PathPrefix] = true
		child := *r
		child.Rules = nil
		child.Upstream, child.CallerAuth, child.SetHeaders, child.Allow = rule.Upstream, rule.CallerAuth, rule.SetHeaders, rule.Allow
		if err := validatePolicy(&child); err != nil {
			return activeRoute{}, err
		}
		ar, err := compileDestination(&child, secrets, discovery)
		if err != nil {
			return activeRoute{}, fmt.Errorf("rule %s: %w", rule.PathPrefix, err)
		}
		rule.CallerAuth, rule.Allow = child.CallerAuth, child.Allow
		ar.pathPrefix = rule.PathPrefix
		parent.rules = append(parent.rules, ar)
		credentials[http.CanonicalHeaderKey(child.CallerAuth.Header)] = true
		for key := range child.SetHeaders {
			credentials[http.CanonicalHeaderKey(key)] = true
		}
	}
	parent.Route = *r
	parent.authority, parent.legacySRVAuthority = rulesAuthorities(parent.rules, r.Redirects)
	for key := range credentials {
		parent.credentialHeaders = append(parent.credentialHeaders, key)
	}
	sort.Strings(parent.credentialHeaders)
	for i := range parent.rules {
		parent.rules[i].authority = parent.authority
		parent.rules[i].credentialHeaders = parent.credentialHeaders
	}
	return parent, nil
}

func compileDestination(r *Route, secrets map[string]material, discovery *upstreamDiscovery) (activeRoute, error) {
	if r.Profile == "cloudflare-clef" && r.Upstream == "" {
		return activeRoute{}, cloudflareRootError()
	}
	upstream, err := discovery.resolve(r.Upstream)
	if err != nil {
		return activeRoute{}, fmt.Errorf("resolve upstream: %w", err)
	}
	origin, err := Origin(upstream)
	if err != nil {
		return activeRoute{}, err
	}
	u, _ := url.Parse(upstream)
	if r.Profile == "cloudflare-clef" && !isCloudflareClefRoot(u) {
		return activeRoute{}, cloudflareRootError()
	}
	if u.Scheme == "http" && !r.AllowHTTP {
		return activeRoute{}, fmt.Errorf("HTTP upstream requires allow_insecure_http")
	}
	if !safePath("/" + strings.TrimPrefix(u.EscapedPath(), "/")) {
		return activeRoute{}, fmt.Errorf("upstream base path is not canonical")
	}
	if r.CallerAuth.Header == "" {
		r.CallerAuth = Auth{Header: "Authorization", Prefix: "Bearer "}
	}
	if !validHeader(r.CallerAuth.Header) || !httpguts.ValidHeaderFieldValue(r.CallerAuth.Prefix) {
		return activeRoute{}, fmt.Errorf("invalid local authentication header")
	}
	headers, err := compileHeaders(*r, secrets)
	if err != nil {
		return activeRoute{}, err
	}
	// Request-time integrations (e.g. accounting) use the concrete URL. The
	// configured reference remains untouched in snapshot.config.
	active := *r
	if active.Upstream != upstream {
		active.Upstream = upstream
		defaultRouteUsage(&active)
	}
	scope := routeScope(*r)
	return activeRoute{Route: active, target: u, origin: origin, authority: scope.Destination(r.Upstream, origin, u),
		legacySRVAuthority: scope.LegacySRV(r.Upstream), headers: headers}, nil
}

func cloudflareRootError() error {
	return fmt.Errorf("Cloudflare Clef requires a full Workers AI run or AI Gateway Workers AI upstream root with IDs filled in")
}

func isCloudflareClefRoot(u *url.URL) bool {
	if u.ForceQuery || strings.ContainsAny(u.Path, "{}") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(u.EscapedPath(), "/"), "/")
	switch strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")) {
	case "api.cloudflare.com":
		return len(parts) == 7 && strings.Join(parts[1:4], "/") == "client/v4/accounts" && parts[4] != "" && parts[5] == "ai" && parts[6] == "run"
	case "gateway.ai.cloudflare.com":
		return len(parts) == 5 && parts[1] == "v1" && parts[2] != "" && parts[3] != "" && parts[4] == "workers-ai"
	default:
		return true // A compatible operator-supplied gateway can mount at any path.
	}
}

func rulesAuthorities(routes []activeRoute, redirects string) (current, legacy string) {
	scopes, old := make([]grantscope.Rule, 0, len(routes)), make([]grantscope.Rule, 0, len(routes))
	hasLegacy := false
	for _, r := range routes {
		scopes = append(scopes, grantscope.Rule{PathPrefix: r.pathPrefix, Authority: r.authority})
		old = append(old, grantscope.Rule{PathPrefix: r.pathPrefix, Authority: cmp.Or(r.legacySRVAuthority, r.authority)})
		hasLegacy = hasLegacy || r.legacySRVAuthority != ""
	}
	if hasLegacy {
		legacy = grantscope.Composite(old, redirects)
	}
	return grantscope.Composite(scopes, redirects), legacy
}

// selectRoute matches before authentication or policy checks; a denial must
// never fall back to a less-specific rule (including the root rule).
func (s *snapshot) selectRoute(escaped string) *activeRoute {
	for i := range s.routes {
		parent := &s.routes[i]
		if !parent.Enabled || !strings.HasPrefix(escaped, parent.LocalPrefix) {
			continue
		}
		if len(parent.rules) == 0 {
			return parent
		}
		p := "/" + strings.TrimPrefix(escaped, parent.LocalPrefix)
		var selected *activeRoute
		for j := range parent.rules {
			rule := &parent.rules[j]
			if (strings.HasPrefix(p, rule.pathPrefix) || p == strings.TrimSuffix(rule.pathPrefix, "/")) && (selected == nil || len(rule.pathPrefix) > len(selected.pathPrefix)) {
				selected = rule
			}
		}
		return selected
	}
	return nil
}

func (r activeRoute) upstreamPath(escaped string) string {
	p := strings.TrimPrefix(escaped, r.LocalPrefix)
	if r.pathPrefix != "" && r.pathPrefix != "/" {
		p = strings.TrimPrefix(p, strings.Trim(r.pathPrefix, "/"))
		p = strings.TrimPrefix(p, "/")
	}
	return p
}

// Strip every protocol's credential slots, including trailers, before setting
// the selected rule's upstream headers. A sibling protocol must not leak a local
// token or a caller-supplied vendor key into this destination.
func stripRouteHeaders(h http.Header, r activeRoute) {
	stripHeaders(h, r.CallerAuth.Header)
	for _, key := range r.credentialHeaders {
		h.Del(key)
	}
}

func (r activeRoute) destinations() []activeRoute {
	if len(r.rules) != 0 {
		return r.rules
	}
	return []activeRoute{r}
}
