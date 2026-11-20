package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

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
	parent.authority = rulesAuthority(parent.rules, r.Redirects)
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
	upstream, err := discovery.resolve(r.Upstream)
	if err != nil {
		return activeRoute{}, fmt.Errorf("resolve upstream: %w", err)
	}
	origin, err := Origin(upstream)
	if err != nil {
		return activeRoute{}, err
	}
	u, _ := url.Parse(upstream)
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
	return activeRoute{Route: active, target: u, origin: origin, authority: routeAuthority(active, origin, u), headers: headers}, nil
}

// Rule order and local credential formatting do not change the delegated scope.
// Each destination hash includes outbound headers, account, WebSocket and allow;
// the mapping and shared redirect policy are included here. Legacy hashes stay
// unchanged because single-destination routes do not use this envelope.
func rulesAuthority(routes []activeRoute, redirects string) string {
	type scope struct{ PathPrefix, Authority string }
	scopes := make([]scope, 0, len(routes))
	for _, r := range routes {
		scopes = append(scopes, scope{r.pathPrefix, r.authority})
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].PathPrefix < scopes[j].PathPrefix })
	if redirects == "" {
		redirects = "passthrough"
	}
	b, _ := json.Marshal(struct {
		Rules     []scope
		Redirects string
	}{scopes, redirects})
	return hashToken(string(b))
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
