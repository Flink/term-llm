package proxy

import "net/http"

// claudeVaultRoundTrip is the accountTransport extension hook. Call after its
// ordinary account/origin/path policy checks, but before ensureAccount: a native
// integration slot needs no shared identity, readiness, or token refresh.
func (t *accountTransport) claudeVaultRoundTrip(req *http.Request, a accountRecord) (*http.Response, bool, error) {
	scope, bound := req.Context().Value(claudeForwardScopeKey{}).(claudeForwardScope)
	if !bound {
		return nil, false, nil
	}
	if a.Kind != "claude-code" || scope.route != t.route.Name || scope.authority != t.route.authority || scope.account != a.Name || scope.identity != a.identityVersion || req.URL.Scheme != "https" || req.URL.Host != claudeAPIHost || req.Context().Err() != nil {
		return nil, true, errAccountUnavailable
	}
	s := t.service
	// Native callers are rechecked at dispatch as well as ordinary admission.
	var caller string
	if s.store.db.QueryRowContext(req.Context(), "SELECT c.id FROM callers c JOIN grants g ON g.caller=c.id WHERE c.id=? AND c.revoked=0 AND g.route=? AND g.authority=?", scope.caller, scope.route, scope.authority).Scan(&caller) != nil {
		return nil, true, errAccountUnavailable
	}
	forwarder, err := s.claudeForwarder()
	if err != nil {
		return nil, true, errAccountUnavailable
	}
	access, err := s.store.claudeVaultAccess(req.Context(), forwarder.ca.vaultKey, scope)
	if err != nil {
		return nil, true, errAccountUnavailable
	}
	t.used = []string{access}
	t.identityVersion = a.identityVersion
	trailers := req.Trailer
	req = req.Clone(req.Context())
	req.Trailer = trailers
	for key := range req.Header {
		if accountControlledHeader(key) {
			delete(req.Header, key)
		}
	}
	for key := range req.Trailer {
		if accountControlledHeader(key) {
			delete(req.Trailer, key)
		}
	}
	req.Header.Set("Authorization", "Bearer "+access)
	if !headerHasToken(req.Header, "Anthropic-Beta", "oauth-2025-04-20") {
		beta := req.Header.Get("Anthropic-Beta")
		if beta != "" {
			beta += ","
		}
		req.Header.Set("Anthropic-Beta", beta+"oauth-2025-04-20")
	}
	req.GetBody = nil
	t.dispatched = true
	resp, err := t.base.RoundTrip(req)
	if resp != nil {
		resp.Header.Del("X-Term-LLM-Proxy-Upstream-Auth")
		if resp.StatusCode == http.StatusUnauthorized {
			// The native client alone performs refresh using its own vault placeholder.
			// Never touch the shared account or replay a potentially charged inference.
			resp.Header.Set("X-Term-LLM-Proxy-Upstream-Auth", "login-required")
		}
	}
	return resp, true, err
}

// A native stream binds to an account identity epoch, not shared credentials.
// Parent trackPolicyStream may use this in place of the shared ready-state test.
func (t *accountTransport) claudeVaultStreamIdentity(a accountRecord) bool {
	return t != nil && a.Kind == "claude-code" && a.identityVersion == t.identityVersion && t.dispatched
}
