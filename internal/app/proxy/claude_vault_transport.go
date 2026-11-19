package proxy

import (
	"errors"
	"net/http"
)

// claudeVaultRoundTrip is the accountTransport extension hook. Call after its
// ordinary account/origin/path policy checks. Native accounts use their own
// encrypted shared vault instead of the generic OAuth account loader.
func (t *accountTransport) claudeVaultRoundTrip(req *http.Request, a accountRecord) (*http.Response, bool, error) {
	scope, bound := req.Context().Value(claudeForwardScopeKey{}).(claudeForwardScope)
	if !bound {
		return nil, false, nil
	}
	provider := scope.nativeProvider()
	if a.Kind != provider.kind || scope.route != t.route.Name || scope.authority != t.route.authority || scope.account != a.Name || scope.identity != a.identityVersion || req.URL.Scheme != "https" || req.URL.Host != provider.apiHost || req.Context().Err() != nil {
		return nil, true, errAccountUnavailable
	}
	s := t.service
	forwarder, err := s.nativeForwarder(provider)
	if err != nil {
		return nil, true, errAccountUnavailable
	}
	key := forwarder.ca.vaultKey
	a, access, err := s.resolveNativeAccess(req, key, scope)
	if err != nil {
		return nil, true, err
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
	if provider == claudeProxyProvider && !headerHasToken(req.Header, "Anthropic-Beta", "oauth-2025-04-20") {
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
			status := "login-required"
			// Refresh for the next request; never replay a potentially charged one.
			_, _, refreshErr := s.claudeSharedAccess(req.Context(), key, scope, a.Generation)
			if refreshErr == nil {
				status = "refreshed"
			} else if errors.Is(refreshErr, errAccountRefreshLimited) {
				status = "refresh-limited"
			}
			resp.Header.Set("X-Term-LLM-Proxy-Upstream-Auth", status)
		}
	}
	return resp, true, err
}

// Recheck the grant after rotation, which can outlast its triggering worker.
func (s *Service) resolveNativeAccess(r *http.Request, key [32]byte, scope claudeForwardScope) (accountRecord, string, error) {
	ctx := r.Context()
	if err := claudeVaultBindingValid(ctx, s.store.db, scope); err != nil {
		return accountRecord{}, "", errAccountUnavailable
	}
	a, access, err := s.claudeSharedAccess(ctx, key, scope, 0)
	if err != nil || access == "" || ctx.Err() != nil || claudeVaultBindingValid(ctx, s.store.db, scope) != nil {
		return accountRecord{}, "", errAccountUnavailable
	}
	return a, access, nil
}
