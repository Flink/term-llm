// Package debugheaders produces bounded, redacted proxy HTTP header previews.
package debugheaders

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

const (
	Request               = "request.headers"
	UpstreamRequest       = "upstream-request.headers"
	UpstreamResponse      = "upstream-response.headers"
	debugHeaderCountLimit = 48
	debugHeaderNameLimit  = 100
	debugHeaderValueLimit = 256
)

type Field struct {
	Name        string `json:"name"`
	Count       int    `json:"count"`
	NonEmpty    bool   `json:"non_empty"`
	Substituted bool   `json:"substituted,omitempty"`
	Value       string `json:"value,omitempty"`
}

type Summary struct {
	Fields              []Field `json:"fields"`
	OmittedFields       int     `json:"omitted_fields,omitempty"`
	AuthorizationScheme string  `json:"authorization_scheme,omitempty"`
	Status              int     `json:"status,omitempty"`
}

type Transport struct {
	Base          http.RoundTripper
	Observe       func(string, []byte)
	CallerHeader  string
	Substitutions http.Header
	Redact        func(string) string
}

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.Record(UpstreamRequest, req.Header, 0)
	resp, err := t.Base.RoundTrip(req)
	if resp != nil {
		t.Record(UpstreamResponse, resp.Header, resp.StatusCode)
	}
	return resp, err
}

func (t Transport) Record(file string, h http.Header, status int) {
	summary := Summary{Fields: make([]Field, 0, min(len(h), debugHeaderCountLimit)), Status: status}
	for _, name := range sortedDebugHeaderNames(h, t.CallerHeader, t.Substitutions) {
		if len(summary.Fields) == debugHeaderCountLimit || len(name) > debugHeaderNameLimit {
			summary.OmittedFields++
			continue
		}
		values := h[name]
		_, replaced := t.Substitutions[name]
		field := Field{
			Name: t.Redact(name), Count: len(values), NonEmpty: slices.ContainsFunc(values, nonEmptyHeaderValue),
			Substituted: file == UpstreamRequest && replaced,
		}
		if len(values) != 0 {
			field.Value = "[redacted]"
			if file != UpstreamResponse && name == "Authorization" {
				field.Value = debugBearerPreview(values)
			} else if !replaced && !strings.EqualFold(name, t.CallerHeader) && safeDebugHeaderValue(name) {
				field.Value = t.Redact(values[0])
				field.Value = field.Value[:min(len(field.Value), debugHeaderValueLimit)]
			}
		}
		summary.Fields = append(summary.Fields, field)
	}
	if file != UpstreamResponse {
		summary.AuthorizationScheme = debugAuthorizationScheme(h.Values("Authorization"))
	}
	if encoded, err := json.MarshalIndent(summary, "", "  "); err == nil {
		t.Observe(file, append(encoded, '\n'))
	}
}

// Most headers, including unknown headers, auth challenges and session IDs,
// stay redacted. Keep values for a small set of protocol and client diagnostics.
func safeDebugHeaderValue(name string) bool {
	switch name {
	case "Content-Type", "Content-Encoding", "Accept", "Accept-Encoding", "User-Agent", "X-Opencode-Client":
		return true
	}
	return false
}

func sortedDebugHeaderNames(h http.Header, callerHeader string, substitutions http.Header) []string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	priority := func(name string) bool {
		return name == callerHeader || substitutions[name] != nil || importantDebugHeader(name)
	}
	slices.SortFunc(names, func(a, b string) int {
		if x, y := priority(a), priority(b); x != y {
			if x {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return names
}

func importantDebugHeader(name string) bool {
	switch name {
	case "Authorization", "X-Api-Key", "X-Goog-Api-Key", "X-Opencode-Session", "Www-Authenticate":
		return true
	}
	return safeDebugHeaderValue(name)
}

func nonEmptyHeaderValue(value string) bool { return strings.TrimSpace(value) != "" }

func debugBearerPreview(values []string) string {
	if debugAuthorizationScheme(values) == "bearer" {
		_, raw, _ := strings.Cut(strings.TrimSpace(values[0]), " ")
		token := strings.TrimSpace(raw)
		// Short or malformed values may be test keys, placeholders, or whole
		// secrets. Only expose a prefix of a plausibly structured bearer token.
		if len(token) >= 12 && strings.IndexFunc(token, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~+/=", r))
		}) < 0 {
			return "Bearer " + token[:3] + "…"
		}
		return "Bearer [redacted]"
	}
	return "[redacted]"
}

func debugAuthorizationScheme(values []string) string {
	if len(values) != 1 {
		return ""
	}
	scheme, rest, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !found || !nonEmptyHeaderValue(rest) {
		return "missing-scheme-or-credential"
	}
	switch scheme = strings.ToLower(scheme); scheme {
	case "bearer", "basic", "aws4-hmac-sha256":
		return scheme
	default:
		return "other"
	}
}
