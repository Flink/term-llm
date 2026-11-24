package usagetap

import (
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// IsExaRoot identifies the public Exa API root, not compatible gateways.
func IsExaRoot(raw string) bool {
	return isProviderRoot(raw, "api.exa.ai", "")
}

func isProviderRoot(raw, host, path string) bool {
	u, err := url.Parse(raw)
	return err == nil && isProviderOrigin(u, host) && u.RawQuery == "" && !u.ForceQuery && strings.TrimSuffix(u.EscapedPath(), "/") == path
}

func isProviderOrigin(u *url.URL, host string) bool {
	if u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || !strings.EqualFold(u.Hostname(), host) {
		return false
	}
	port, _ := strconv.Atoi(u.Port())
	return u.Port() == "" || port == 443
}

// IsExaSearchJSON checks the upstream and the actual response request before
// treating a payload as a first-party Exa cost estimate.
func IsExaSearchJSON(upstream string, resp *http.Response) bool {
	return isOfficialJSON(upstream, resp, "api.exa.ai", "/search")
}

// IsTypeSafeRoot identifies the public TypeSafe API root, not compatible gateways.
func IsTypeSafeRoot(raw string) bool { return isProviderRoot(raw, "api.typesafe.ai", "") }

// IsVeniceRoot identifies the public Venice API root.
func IsVeniceRoot(raw string) bool { return isProviderRoot(raw, "api.venice.ai", "/api/v1") }

// IsMistralRoot identifies the public Mistral API root.
func IsMistralRoot(raw string) bool { return isProviderRoot(raw, "api.mistral.ai", "/v1") }

// IsMistralChatResponse scopes Mistral-specific accounting to its official chat API.
func IsMistralChatResponse(upstream string, resp *http.Response) bool {
	return IsMistralRoot(upstream) && isOfficialResponse(resp, "api.mistral.ai", "/v1/chat/completions") && resp.Request.Method == http.MethodPost
}

// IsVeniceChatResponse recognizes the actual first-party chat endpoint. The
// transport format is validated separately by the Venice adapter.
func IsVeniceChatResponse(upstream string, resp *http.Response) bool {
	return IsVeniceRoot(upstream) && isOfficialResponse(resp, "api.venice.ai", "/api/v1/chat/completions")
}

func isOfficialJSON(upstream string, resp *http.Response, host, path string) bool {
	if !isProviderRoot(upstream, host, "") || !isOfficialResponse(resp, host, path) || resp.Request.Method != http.MethodPost {
		return false
	}
	ct := resp.Header.Values("Content-Type")
	if len(ct) != 1 {
		return false
	}
	media, _, err := mime.ParseMediaType(ct[0])
	return err == nil && media == "application/json"
}

func isOfficialResponse(resp *http.Response, host, path string) bool {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.Uncompressed {
		return false
	}
	req := resp.Request
	if req == nil || req.URL == nil || !isProviderOrigin(req.URL, host) || req.URL.EscapedPath() != path {
		return false
	}
	return req.Host == "" || isProviderRoot((&url.URL{Scheme: req.URL.Scheme, Host: req.Host}).String(), host, "")
}
