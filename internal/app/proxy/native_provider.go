package proxy

import (
	"net/http"
	"strings"
)

const (
	agyAPIHost     = "daily-cloudcode-pa.googleapis.com"
	agyOAuthHost   = "oauth2.googleapis.com"
	agyProfileHost = "www.googleapis.com"
)

// nativeProxyProvider is a fixed trust policy, not route-configurable data.
// Each provider keeps its own name-constrained CA and vault key; the CONNECT,
// OAuth fencing, and ledger implementation are shared.
type nativeProxyProvider struct {
	kind, label, discovery, caFile, vaultDomain string
	apiHost, tokenURL, clientID                 string
	accessPrefix, refreshPrefix                 string
	hosts                                       []string
}

var claudeProxyProvider = &nativeProxyProvider{
	kind: "claude-bin", label: "Claude", discovery: claudeForwardDiscovery,
	caFile: claudeForwardCAFile, vaultDomain: "term-llm/claude-vault/v2\x00",
	apiHost: claudeAPIHost, tokenURL: claudeTokenURL, clientID: claudeClientID,
	accessPrefix: "sk-ant-oat01-tlv2-", refreshPrefix: "sk-ant-ort01-tlv2-",
	hosts: []string{claudeAPIHost, claudeOAuthHost},
}

var agyProxyProvider = &nativeProxyProvider{
	kind: "agy-bin", label: "Antigravity", discovery: ".well-known/term-llm/agy-proxy",
	caFile: "agy-forward-ca-v2.pem", vaultDomain: "term-llm/agy-vault/v2\x00",
	apiHost: agyAPIHost, tokenURL: "https://" + agyOAuthHost + "/token",
	clientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
	accessPrefix: "agy-access-tlv2-", refreshPrefix: "agy-refresh-tlv2-",
	hosts: []string{agyAPIHost, agyOAuthHost, agyProfileHost},
}

var nativeProxyProviders = []*nativeProxyProvider{claudeProxyProvider, agyProxyProvider}

func nativeProviderForKind(kind string) *nativeProxyProvider {
	for _, p := range nativeProxyProviders {
		if p.kind == kind {
			return p
		}
	}
	return nil
}

func (p *nativeProxyProvider) permitsHost(host string) bool {
	for _, allowed := range p.hosts {
		if host == allowed {
			return true
		}
	}
	return false
}

func nativeProviderForAuthority(authority string) *nativeProxyProvider {
	host, ok := strings.CutSuffix(authority, ":443")
	if ok {
		for _, p := range nativeProxyProviders {
			if p.permitsHost(host) {
				return p
			}
		}
	}
	return nil
}

func agyNativeAPIRequest(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.ForceQuery {
		return false
	}
	switch r.URL.EscapedPath() {
	case "/v1internal:streamGenerateContent":
		return r.URL.RawQuery == "alt=sse" || r.URL.RawQuery == ""
	case "/v1internal:generateContent", "/v1internal:loadCodeAssist", "/v1internal:fetchUserInfo",
		"/v1internal:retrieveUserQuotaSummary", "/v1internal:fetchAvailableModels", "/v1internal:fetchAdminControls", "/v1internal:listExperiments":
		return r.URL.RawQuery == ""
	}
	return false
}

func (s claudeForwardScope) nativeProvider() *nativeProxyProvider {
	if s.provider == nil {
		return claudeProxyProvider
	}
	return s.provider
}

func (p *nativeProxyProvider) validOAuthPayload(payload claudeVaultPayload) bool {
	if payload.value("client_id") != p.clientID {
		return false
	}
	if p == agyProxyProvider {
		return agyNativeOAuthPayload(payload)
	}
	return true
}

func agyNativeOAuthPayload(payload claudeVaultPayload) bool {
	if payload.form == nil || payload.value("client_secret") == "" {
		return false
	}
	grant := payload.value("grant_type")
	if grant == "authorization_code" && payload.value("redirect_uri") != "https://antigravity.google/oauth-callback" {
		return false
	}
	for key := range payload.form {
		switch key {
		case "grant_type", "client_id", "client_secret":
		case "code", "code_verifier", "redirect_uri":
			if grant != "authorization_code" {
				return false
			}
		case "refresh_token":
			if grant != "refresh_token" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func agyNativeOAuthScopes(raw string) string {
	var scopes []string
	for _, scope := range strings.Fields(raw) {
		switch scope {
		case "openid", "https://www.googleapis.com/auth/cloud-platform", "https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile", "https://www.googleapis.com/auth/cclog",
			"https://www.googleapis.com/auth/experimentsandconfigs", "https://www.googleapis.com/auth/aicode":
			scopes = append(scopes, scope)
		}
	}
	return strings.Join(scopes, " ")
}

func (p *nativeProxyProvider) allowsNativeAPI(r *http.Request) bool {
	if p == agyProxyProvider {
		return agyNativeAPIRequest(r)
	}
	return claudeSharedRequestAllowed(r)
}
