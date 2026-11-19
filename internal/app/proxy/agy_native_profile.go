package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const agyProfilePath = "/oauth2/v2/userinfo"

// agy needs userinfo to finish login and reopens it while loading its account.
// The Cloud Code OAuth credential is never sent to another Google API: rebuild
// the exact request and return only the fields the identity endpoint documents.
func (s *Service) agyNativeProfile(w http.ResponseWriter, r *http.Request, key [32]byte, scope claudeForwardScope) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet || r.URL.EscapedPath() != agyProfilePath || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 {
		http.Error(w, "unsupported Antigravity identity request", 403)
		return
	}
	if !s.claudeForwardRequestSlot(w) {
		return
	}
	defer func() { <-s.slots }()
	ctx, cancel := context.WithTimeout(r.Context(), min(claudeVaultOAuthTimeout, s.opts.RequestTimeout))
	defer cancel()
	a, access, err := s.resolveNativeAccess(r.WithContext(ctx), key, scope)
	if err != nil {
		http.Error(w, "native login required", http.StatusUnauthorized)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+agyProfileHost+agyProfilePath, nil)
	if err != nil {
		http.Error(w, "Antigravity identity unavailable", 502)
		return
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := s.transport.RoundTrip(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		http.Error(w, "Antigravity identity unavailable", 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		_, _, _ = s.claudeSharedAccess(ctx, key, scope, a.Generation)
		http.Error(w, "native login required", 401)
		return
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		http.Error(w, "Antigravity identity unavailable", 502)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, claudeVaultMaxBody+1))
	var identity struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
		Name          string `json:"name,omitempty"`
		GivenName     string `json:"given_name,omitempty"`
		FamilyName    string `json:"family_name,omitempty"`
		Picture       string `json:"picture,omitempty"`
		Locale        string `json:"locale,omitempty"`
		HostedDomain  string `json:"hd,omitempty"`
	}
	if err != nil || len(body) > claudeVaultMaxBody || strings.Contains(string(body), access) || json.Unmarshal(body, &identity) != nil {
		http.Error(w, "invalid Antigravity identity reply", 502)
		return
	}
	jsonReply(w, 200, identity)
}
