package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const claudeVaultMaxBody = 64 << 10
const claudeVaultOAuthTimeout = 30 * time.Second

// Only an operator enrollment can exchange a code. Client-side refresh only
// reissues scoped aliases; the shared account owns upstream token rotation.
func (s *Service) claudeVaultOAuth(w http.ResponseWriter, r *http.Request, tunnel *claudeNativeTunnel, scope claudeForwardScope) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(r.Header.Values("Authorization")) != 0 || len(r.Trailer) != 0 {
		claudeVaultError(w, 400, "invalid_request")
		return
	}
	if !s.requestSlot(w) {
		return
	}
	defer func() { <-s.slots }()
	ctx, cancel := context.WithTimeout(r.Context(), claudeVaultOAuthTimeout)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(claudeVaultOAuthTimeout))
	defer controller.SetReadDeadline(time.Time{})
	limit := int64(claudeVaultMaxBody)
	if s.opts.MaxBody < limit {
		limit = s.opts.MaxBody
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		claudeVaultError(w, 413, "invalid_request")
		return
	}
	provider := scope.nativeProvider()
	payload, err := parseClaudeVaultPayload(r.Header.Get("Content-Type"), body)
	if err != nil || !provider.validOAuthPayload(payload) {
		claudeVaultError(w, 400, "invalid_request")
		return
	}
	s.claudeSharedOAuth(w, r.WithContext(ctx), tunnel, scope, payload)
}

type nativeOAuthError struct {
	status int
	code   string
}

func (e nativeOAuthError) Error() string { return e.code }

func writeNativeOAuthError(w http.ResponseWriter, err error) {
	if e, ok := err.(nativeOAuthError); ok {
		claudeVaultError(w, e.status, e.code)
		return
	}
	claudeVaultError(w, 503, "login_required")
}

func (s *Service) exchangeNativeOAuth(ctx context.Context, provider *nativeProxyProvider, contentType string, body []byte, previous *claudeVaultRecord) (claudeVaultReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.tokenURL, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return claudeVaultReply{}, nativeOAuthError{502, "login_required"}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.ContentLength = int64(len(body))
	req.GetBody = nil // Never replay a code or rotating refresh, including empty/safe requests.
	resp, err := s.transport.RoundTrip(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return claudeVaultReply{}, nativeOAuthError{502, "login_required"}
	}
	defer resp.Body.Close()
	// Do not read, log, reflect or follow issuer error/redirect bodies or headers.
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" {
		return claudeVaultReply{}, nativeOAuthError{400, "invalid_grant"}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, claudeVaultMaxBody+1))
	if err != nil || len(data) > claudeVaultMaxBody {
		return claudeVaultReply{}, nativeOAuthError{502, "login_required"}
	}
	reply, err := provider.parseVaultReply(data, previous)
	if err != nil {
		return claudeVaultReply{}, nativeOAuthError{400, "subscription_login_required"}
	}
	return reply, nil
}

func claudeVaultError(w http.ResponseWriter, status int, code string) {
	// All arguments are static caller-owned strings, never issuer text.
	jsonReply(w, status, map[string]string{"error": code, "error_description": "Native subscription login required; sign in again."})
}

type claudeVaultPayload struct {
	body        []byte
	contentType string
	object      map[string]json.RawMessage
	form        url.Values
}

func parseClaudeVaultPayload(contentType string, body []byte) (claudeVaultPayload, error) {
	p := claudeVaultPayload{body: body}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return p, errClaudeVaultLogin
	}
	p.contentType = media
	switch media {
	case "application/json":
		// Reject duplicate top-level keys, rather than forwarding a credential that
		// the issuer could interpret differently from the one fenced in this process.
		decoder := json.NewDecoder(bytes.NewReader(body))
		token, e := decoder.Token()
		if e != nil || token != json.Delim('{') {
			return p, errClaudeVaultLogin
		}
		p.object = map[string]json.RawMessage{}
		for decoder.More() {
			key, e := decoder.Token()
			if e != nil {
				return p, errClaudeVaultLogin
			}
			name, ok := key.(string)
			if !ok {
				return p, errClaudeVaultLogin
			}
			if _, exists := p.object[name]; exists {
				return p, errClaudeVaultLogin
			}
			var value json.RawMessage
			if decoder.Decode(&value) != nil {
				return p, errClaudeVaultLogin
			}
			p.object[name] = value
		}
		if _, e = decoder.Token(); e != nil {
			return p, errClaudeVaultLogin
		}
		if decoder.Decode(new(any)) != io.EOF {
			return p, errClaudeVaultLogin
		}
	case "application/x-www-form-urlencoded":
		p.form, err = url.ParseQuery(string(body))
		if err != nil {
			return p, errClaudeVaultLogin
		}
		for _, values := range p.form {
			if len(values) != 1 {
				return p, errClaudeVaultLogin
			}
		}
	default:
		return p, errClaudeVaultLogin
	}
	return p, nil
}
func (p claudeVaultPayload) value(key string) string {
	if p.form != nil {
		return p.form.Get(key)
	}
	var v string
	if json.Unmarshal(p.object[key], &v) != nil {
		return ""
	}
	return v
}
func parseClaudeVaultReply(data []byte) (claudeVaultReply, error) {
	return parseClaudeVaultReplyForRefresh(data, nil)
}

func parseClaudeVaultReplyForRefresh(data []byte, previous *claudeVaultRecord) (claudeVaultReply, error) {
	return claudeProxyProvider.parseVaultReply(data, previous)
}

func (provider *nativeProxyProvider) parseVaultReply(data []byte, previous *claudeVaultRecord) (claudeVaultReply, error) {
	var reply claudeVaultReply
	if json.Unmarshal(data, &reply) != nil {
		return reply, errClaudeVaultLogin
	}
	// RFC 6749 refresh responses may omit these fields to retain their existing
	// values. Only encrypted, previously validated custody may supply them, never
	// client-requested scopes. Explicit empty/null values still fail validation.
	if previous != nil && previous.state == "ready" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return reply, errClaudeVaultLogin
		}
		if _, present := fields["refresh_token"]; !present {
			reply.RefreshToken = previous.private.Refresh
		}
		if _, present := fields["scope"]; !present {
			reply.Scope = previous.private.Scope
		}
	}
	if !validClaudeVaultToken(reply.AccessToken) || !validClaudeVaultToken(reply.RefreshToken) ||
		!strings.EqualFold(reply.TokenType, "bearer") || reply.ExpiresIn <= 0 || reply.ExpiresIn > 366*24*60*60 || len(reply.Scope) > 4096 {
		return reply, errClaudeVaultLogin
	}
	reply.TokenType = "Bearer"
	if provider == agyProxyProvider {
		// Google may include an ID token and extra metadata. Neither is needed
		// by agy, and the only client-visible credentials must be placeholders.
		reply.Account, reply.Organization = nil, nil
		reply.Scope = agyNativeOAuthScopes(reply.Scope)
		return reply, nil
	}
	profile, inference := false, false
	var scopes []string
	seen := map[string]bool{}
	for _, scope := range strings.Fields(reply.Scope) {
		switch scope {
		case "org:create_api_key":
			return reply, errClaudeVaultLogin
		case "user:profile":
			profile = true
		case "user:inference":
			inference = true
		case "user:sessions:claude_code", "user:mcp_servers", "user:file_upload", "user:plugins":
		default:
			continue
		}
		if !seen[scope] {
			scopes = append(scopes, scope)
			seen[scope] = true
		}
	}
	if !profile || !inference {
		return reply, errClaudeVaultLogin
	}
	reply.Scope = strings.Join(scopes, " ")
	if err := sanitizeClaudeVaultMetadata(&reply, previous); err != nil {
		return reply, err
	}
	return reply, nil
}
func validClaudeVaultToken(token string) bool {
	if token == "" || len(token) > 16<<10 || strings.Contains(token, "tlv2-") {
		return false
	}
	for _, c := range token {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}
