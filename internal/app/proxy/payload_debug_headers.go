package proxy

import (
	"net/http"
	"net/url"

	"github.com/term-llm/term-llm/internal/app/proxy/debugheaders"
)

func (s *Service) debugTransport(base http.RoundTripper, c *payloadCapture, r *http.Request, ar activeRoute, snap *snapshot) http.RoundTripper {
	if c == nil {
		return base
	}
	token := callerCredential(r.Header, ar.CallerAuth.Header, ar.CallerAuth.Prefix)
	transport := debugheaders.Transport{
		Base: base, CallerHeader: ar.CallerAuth.Header, Substitutions: ar.headers,
		Redact:  func(value string) string { return s.redactPath(url.PathEscape(value), token, snap) },
		Observe: func(file string, data []byte) { c.observe(file, data, true, false, false) },
	}
	transport.Record(debugheaders.Request, r.Header, 0)
	return transport
}
