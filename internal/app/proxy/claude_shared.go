package proxy

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Bootstrap returns only a scoped alias. The operator's one-time grant gets
// classic bootstrap so that its fresh profile can perform the native login.
func (s *Service) claudeSharedBootstrap(w http.ResponseWriter, r *http.Request, f *claudeForwarder, a accountRecord, scope claudeForwardScope, metadata map[string]any) bool {
	provider := scope.nativeProvider()
	if claudeSharedEnrollmentActive(r.Context(), s.store.db, scope) {
		return true
	}
	if a.State != "ready" && a.State != "refreshing" {
		http.Error(w, "shared "+provider.label+" subscription not connected; ask the proxy operator to sign in", http.StatusConflict)
		return false
	}
	secret, err := claudeSharedSecret(f.ca.vaultKey, a)
	if err != nil || secret.Access == "" {
		http.Error(w, "shared "+provider.label+" subscription credentials unavailable", http.StatusServiceUnavailable)
		return false
	}
	metadata["access_placeholder"] = claudeSharedAlias(f.ca.vaultKey, a, scope, provider.accessPrefix)
	if provider == agyProxyProvider {
		metadata["refresh_placeholder"] = claudeSharedAlias(f.ca.vaultKey, a, scope, provider.refreshPrefix)
		metadata["expires_in"] = nativeAliasLifetime
	}
	metadata["scopes"] = secret.Scope
	return true
}

func validClaudeSharedAlias(key [32]byte, a accountRecord, scope claudeForwardScope, prefix string) bool {
	return a.claudeShared() && hmac.Equal([]byte(claudeSharedAlias(key, a, scope, prefix)), []byte(scope.access))
}

func (s *Service) claudeSharedOAuth(w http.ResponseWriter, r *http.Request, tunnel *claudeNativeTunnel, scope claudeForwardScope, payload claudeVaultPayload) {
	key := tunnel.forwarder.ca.vaultKey
	provider := scope.nativeProvider()
	ctx, cancel := context.WithTimeout(r.Context(), claudeVaultOAuthTimeout)
	defer cancel()
	var reply claudeVaultReply
	var err error
	switch payload.value("grant_type") {
	case "authorization_code":
		if payload.value("code") == "" || payload.value("code_verifier") == "" || payload.value("redirect_uri") == "" || payload.value("refresh_token") != "" ||
			(provider == agyProxyProvider && !validClaudeVaultToken(payload.value("client_secret"))) {
			claudeVaultError(w, 400, "invalid_request")
			return
		}
		reply, err = s.connectClaudeShared(ctx, key, scope, payload)
	case "refresh_token":
		// The interactive operator may keep the profile created by native login
		// open. Its local refresh only renews the alias lifetime, never forces
		// a provider rotation (nor allows workers to set scopes or expiry).
		scope.access = payload.value("refresh_token")
		reply, err = s.claudeSharedLocalRefresh(ctx, key, scope)
	default:
		err = nativeOAuthError{400, "unsupported_grant_type"}
	}
	if err != nil {
		writeNativeOAuthError(w, err)
		return
	}
	jsonReply(w, 200, reply)
}

func (s *Service) connectClaudeShared(ctx context.Context, key [32]byte, scope claudeForwardScope, payload claudeVaultPayload) (claudeVaultReply, error) {
	// Do not let an ordinary caller contend for the per-account issuer lock.
	if !claudeSharedEnrollmentActive(ctx, s.store.db, scope) {
		return claudeVaultReply{}, nativeOAuthError{403, "operator_login_required"}
	}
	unlock, err := s.accountLock(ctx, scope.account)
	if err != nil {
		return claudeVaultReply{}, err
	}
	defer unlock()
	a, err := s.store.account(scope.account)
	if err != nil || !a.claudeShared() || a.identityVersion != scope.identity || s.store.reserveClaudeSharedEnrollment(ctx, scope) != nil {
		return claudeVaultReply{}, nativeOAuthError{403, "operator_login_required"}
	}
	reply, err := s.exchangeNativeOAuth(ctx, scope.nativeProvider(), payload.contentType, payload.body, nil)
	if err != nil {
		return reply, err
	}
	next := a
	next.Generation++
	next.identityVersion++
	next.refresh = accountRefreshState{}
	clientSecret := ""
	if scope.nativeProvider() == agyProxyProvider {
		clientSecret = payload.value("client_secret")
	}
	if err = sealNativeAccount(key, &next, reply, clientSecret); err != nil {
		return claudeVaultReply{}, err
	}
	// The reservation and grant must still exist after the issuer response.
	// Once the issuer succeeded, finish the bounded commit even if the CLI left.
	commit, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err = s.finishClaudeSharedEnrollment(commit, scope, a, next); err != nil {
		return claudeVaultReply{}, err
	}
	return claudeSharedAliasReply(key, next, scope, reply), nil
}

// Serialize the commit with route changes. A disabled route may retain grants to
// support re-enable, but cannot complete an operator exchange while disabled.
func (s *Service) finishClaudeSharedEnrollment(ctx context.Context, scope claudeForwardScope, before, after accountRecord) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.closing {
		for _, route := range s.snap.routes {
			if route.Enabled && route.Name == scope.route && route.Account == scope.account && route.authority == scope.authority {
				return s.store.finishClaudeSharedEnrollment(ctx, scope, before, after)
			}
		}
	}
	return errClaudeVaultLogin
}

func claudeSharedAliasReply(key [32]byte, a accountRecord, scope claudeForwardScope, reply claudeVaultReply) claudeVaultReply {
	reply.AccessToken = claudeSharedAlias(key, a, scope, scope.nativeProvider().accessPrefix)
	reply.RefreshToken = claudeSharedAlias(key, a, scope, scope.nativeProvider().refreshPrefix)
	reply.ExpiresIn = nativeAliasLifetime
	return reply
}

func (s *Service) claudeSharedLocalRefresh(ctx context.Context, key [32]byte, scope claudeForwardScope) (claudeVaultReply, error) {
	a, err := s.store.account(scope.account)
	if err != nil || !validClaudeSharedAlias(key, a, scope, scope.nativeProvider().refreshPrefix) || claudeVaultBindingValid(ctx, s.store.db, scope) != nil {
		return claudeVaultReply{}, nativeOAuthError{400, "invalid_grant"}
	}
	// The alias follows the account through rotation and cannot outlive the
	// route grant. Its local TTL is therefore independent of upstream expiry.
	secret, err := claudeSharedSecret(key, a)
	if err != nil || a.State != "ready" && a.State != "refreshing" {
		return claudeVaultReply{}, nativeOAuthError{400, "invalid_grant"}
	}
	return claudeSharedAliasReply(key, a, scope, claudeVaultReply{Scope: secret.Scope, TokenType: "Bearer"}), nil
}

// Use an explicit provider surface for delegated access. Per-client vault
// routes may expose other native endpoints under the user's own identity.
func claudeSharedRequestAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost:
		return r.URL.EscapedPath() == "/v1/messages" || r.URL.EscapedPath() == "/v1/messages/count_tokens"
	case http.MethodGet, http.MethodHead:
		switch r.URL.EscapedPath() {
		case "/v1/models", "/api/oauth/profile", "/api/oauth/usage", "/api/claude_code/settings", "/api/claude_code/policy_limits":
			return true
		}
	}
	return false
}

func (s *Service) claudeSharedAccess(ctx context.Context, key [32]byte, scope claudeForwardScope, rejected int64) (accountRecord, string, error) {
	a, err := s.store.account(scope.account)
	if err != nil || !validClaudeSharedAlias(key, a, scope, scope.nativeProvider().accessPrefix) || a.identityVersion != scope.identity {
		return a, "", errAccountUnavailable
	}
	if rejected == 0 && a.State == "ready" && a.refresh.RejectedGeneration != a.Generation && a.ExpiresAt > time.Now().Add(time.Minute).Unix() {
		secret, err := claudeSharedSecret(key, a)
		return a, secret.Access, err
	}
	unlock, err := s.accountLock(ctx, a.Name)
	if err != nil {
		return a, "", err
	}
	defer unlock()
	a, err = s.store.account(scope.account)
	if err != nil || a.State != "ready" || !validClaudeSharedAlias(key, a, scope, scope.nativeProvider().accessPrefix) || claudeVaultBindingValid(ctx, s.store.db, scope) != nil {
		return a, "", errAccountUnavailable
	}
	secret, err := claudeSharedSecret(key, a)
	if err != nil || secret.Access == "" || secret.Refresh == "" {
		return a, "", errAccountUnavailable
	}
	now := time.Now()
	if rejected != 0 && a.Generation != rejected {
		if a.refresh.RejectedGeneration == a.Generation {
			return a, "", errAccountRefreshLimited
		}
		return a, secret.Access, nil
	}
	if rejected == 0 && a.refresh.RejectedGeneration != a.Generation && a.ExpiresAt > now.Add(time.Minute).Unix() {
		return a, secret.Access, nil
	}
	if a.refresh.LastSuccess > now.Add(-time.Minute).UnixMilli() {
		if rejected != 0 && a.refresh.RejectedGeneration != a.Generation {
			next := a
			next.refresh.RejectedGeneration = a.Generation
			if err := s.store.changeClaudeSharedAccount(ctx, a, next); err != nil {
				return a, "", errAccountUnavailable
			}
		}
		if rejected == 0 && a.refresh.RejectedGeneration != a.Generation && a.ExpiresAt > now.Unix() {
			return a, secret.Access, nil
		}
		return a, "", errAccountRefreshLimited
	}
	return s.rotateClaudeShared(ctx, key, a, secret)
}

func (s *Service) rotateClaudeShared(ctx context.Context, key [32]byte, a accountRecord, secret claudeVaultPrivate) (accountRecord, string, error) {
	fence := a
	fence.State = "refreshing"
	if err := s.store.changeClaudeSharedAccount(ctx, a, fence); err != nil {
		return a, "", errAccountUnavailable
	}
	// A rotating credential may be consumed even if a worker disconnects. Fence
	// before sending; any ambiguous outcome remains non-replayable on restart.
	own, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeVaultOAuthTimeout)
	defer cancel()
	provider := nativeProviderForKind(a.Kind)
	contentType, body := nativeRefreshRequest(provider, secret)
	reply, err := s.exchangeNativeOAuth(own, provider, contentType, body, &claudeVaultRecord{state: "ready", private: secret})
	commit, finish := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finish()
	if err != nil {
		failed := fence
		failed.State, failed.claudeSharedCipher, failed.ExpiresAt = "needs_login", nil, 0
		failed.Updated = time.Now().UnixMilli()
		_ = s.store.changeClaudeSharedAccount(commit, fence, failed)
		return a, "", errAccountUnavailable
	}
	next := a
	next.Generation++
	next.refresh.LastSuccess = time.Now().UnixMilli()
	if err := sealNativeAccount(key, &next, reply, secret.ClientSecret); err != nil || s.store.changeClaudeSharedAccount(commit, fence, next) != nil {
		return a, "", errAccountUnavailable
	}
	return next, reply.AccessToken, nil
}

const nativeAliasLifetime = 3600

func nativeRefreshRequest(provider *nativeProxyProvider, secret claudeVaultPrivate) (string, []byte) {
	if provider == agyProxyProvider {
		return "application/x-www-form-urlencoded", []byte(url.Values{
			"grant_type": {"refresh_token"}, "client_id": {provider.clientID},
			"client_secret": {secret.ClientSecret}, "refresh_token": {secret.Refresh},
		}.Encode())
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": provider.clientID, "refresh_token": secret.Refresh})
	return "application/json", body
}
