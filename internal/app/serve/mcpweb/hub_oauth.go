package mcpweb

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	apphub "github.com/term-llm/term-llm/internal/app/serve/hub"
	appserver "github.com/term-llm/term-llm/internal/app/serve/server"
	"github.com/term-llm/term-llm/internal/hub"
	"github.com/term-llm/term-llm/internal/passkeyauth"
)

const (
	HubOAuthCookie    = "term_llm_hub_mcp_oauth"
	hubMCPOAuthResume = "_term_llm_resume"
	hubMCPOAuthKind   = passkeyauth.CeremonyKind("mcp_oauth")
	// Bound the amount retained per continuation as well as the number of entries.
	hubMCPOAuthQueryLimit = 8 << 10
)

// IsHubOAuthCallback reports whether path is an MCP callback under a node mount.
func IsHubOAuthCallback(path string) bool {
	id, rest, ok := apphub.ParseNodePath(path)
	return ok && rest == OAuthCallbackPath && hub.ValidateID(id) == nil
}

// HubOAuthReturnPath carries only the opaque handle through the normal login
// ceremony. A user-supplied return_path cannot carry OAuth response parameters.
func HubOAuthReturnPath(basePath, safePath, raw string) string {
	if !IsHubOAuthCallback(strings.TrimPrefix(safePath, basePath)) {
		return safePath
	}
	_, rawQuery, _ := strings.Cut(raw, "?")
	if handle := hubMCPOAuthResumeHandle(rawQuery); handle != "" {
		return safePath + "?" + hubMCPOAuthResume + "=" + handle
	}
	return safePath
}

func hubMCPOAuthResumeHandle(query string) string {
	handle, ok := strings.CutPrefix(query, hubMCPOAuthResume+"=")
	if !ok || len(handle) != base64.RawURLEncoding.EncodedLen(32) {
		return ""
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(handle); err != nil {
		return ""
	}
	return handle
}

// HubOAuth keeps callback fields off browser-visible login and continuation URLs.
type HubOAuth struct {
	Endpoint           passkeyauth.Endpoint
	Returns            *passkeyauth.Ceremonies
	Peer               func(*http.Request) string
	BootstrapAvailable func(*http.Request) bool
}

// ServeCallback handles a matched Hub MCP callback. Authenticated callers pass
// their prepared node handler as forward; a nil handler cannot reach a node.
func (s HubOAuth) ServeCallback(w http.ResponseWriter, r *http.Request, forward http.Handler, hasBrowserSession bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if appserver.ContainsEncodedSeparator(r.URL.EscapedPath()) {
		http.Error(w, "Invalid OAuth callback path", http.StatusBadRequest)
		return
	}
	if len(r.URL.RawQuery) > hubMCPOAuthQueryLimit {
		http.Error(w, "OAuth callback query is too long", http.StatusRequestURITooLong)
		return
	}
	if handle := hubMCPOAuthResumeHandle(r.URL.RawQuery); handle != "" {
		s.resume(w, r, forward, hasBrowserSession, handle)
		return
	}
	query, err := ParseOAuthCallbackQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Invalid OAuth callback. Start authorization again.", http.StatusBadRequest)
		return
	}
	if forward != nil && apphub.BrowserRequestAllowed(r, false) {
		forward.ServeHTTP(w, hubMCPOAuthRequest(r, query.Encode()))
		return
	}
	if !apphub.ShouldRenderLogin(r) {
		w.Header().Set("X-Term-LLM-Login-URL", s.publicPath("/auth/login"))
		http.Error(w, "Hub passkey authentication is required", http.StatusUnauthorized)
		return
	}
	if s.BootstrapAvailable(r) {
		http.Redirect(w, r, s.publicPath("/auth/setup"), http.StatusSeeOther)
		return
	}
	s.redirectLogin(w, r, query.Encode())
}

func (s HubOAuth) redirectLogin(w http.ResponseWriter, r *http.Request, query string) {
	var random [32]byte
	rand.Read(random[:])
	handle := base64.RawURLEncoding.EncodeToString(random[:])
	// Use a separate registry so public callbacks cannot fill or evict passkey
	// ceremonies. Bind each record to the URL handle, exact route and private
	// browser cookie. Nothing in Meta is returned to the browser.
	pending, err := s.Returns.Create(hubMCPOAuthKind, s.Peer(r), handle, r.URL.Path, query, nil, webauthn.SessionData{})
	if err != nil {
		http.Error(w, "Could not save OAuth callback. Start authorization again.", http.StatusServiceUnavailable)
		return
	}
	s.setCookie(w, pending.CookieToken)
	resume := s.publicPath(r.URL.Path) + "?" + hubMCPOAuthResume + "=" + handle
	target := s.publicPath("/auth/login") + "?return=" + url.QueryEscape(resume)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s HubOAuth) resume(w http.ResponseWriter, r *http.Request, forward http.Handler, hasBrowserSession bool, handle string) {
	cookie, err := r.Cookie(HubOAuthCookie)
	if hasBrowserSession && forward != nil && err == nil && apphub.BrowserRequestAllowed(r, false) {
		pending, err := s.Returns.Consume(cookie.Value, hubMCPOAuthKind, handle, r.URL.Path)
		if err == nil {
			s.setCookie(w, "")
			forward.ServeHTTP(w, hubMCPOAuthRequest(r, pending.Meta))
			return
		}
	}
	http.Error(w, "Invalid or expired OAuth continuation. Start authorization again.", http.StatusBadRequest)
}

func hubMCPOAuthRequest(r *http.Request, query string) *http.Request {
	// Leave the browser-facing RequestURI unchanged. The Hub's node transports
	// forward URL.RawQuery, so only the selected node receives the OAuth fields.
	r = r.Clone(r.Context())
	r.URL.RawQuery = query
	return r
}

func (s HubOAuth) setCookie(w http.ResponseWriter, token string) {
	cookie := &http.Cookie{Name: HubOAuthCookie, Value: token, Path: s.Endpoint.CookiePath, HttpOnly: true,
		Secure: s.Endpoint.Secure, SameSite: http.SameSiteStrictMode, MaxAge: int(passkeyauth.CeremonyLifetime.Seconds())}
	if token == "" {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}

func (s HubOAuth) publicPath(path string) string { return apphub.Path(s.Endpoint.BasePath, path) }
