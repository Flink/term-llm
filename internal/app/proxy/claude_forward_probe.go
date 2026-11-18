package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

const claudeProbeMaxReply = 64 << 10

// Native Claude checks these public endpoints before presenting its login UI.
// The surrounding CONNECT still requires a scoped client and a current grant;
// only these exact credential-free probes bypass vendor OAuth authentication.
func claudeNativeProbe(host string, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return false
	}
	return host == claudeAPIHost && r.URL.EscapedPath() == "/api/hello" ||
		host == claudeOAuthHost && r.URL.EscapedPath() == "/v1/oauth/hello"
}

func (s *Service) claudeForwardProbe(w http.ResponseWriter, r *http.Request, host string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 {
		http.Error(w, "connectivity probes cannot contain a body", http.StatusBadRequest)
		return
	}
	if !s.claudeForwardRequestSlot(w) {
		return
	}
	defer func() { <-s.slots }()
	timeout := 15 * time.Second
	if s.opts.RequestTimeout < timeout {
		timeout = s.opts.RequestTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	// Reconstruct rather than clone: no client/proxy/vendor authorization,
	// cookies, arbitrary headers or query parameters can reach the public probe.
	req, err := http.NewRequestWithContext(ctx, r.Method, "https://"+host+r.URL.EscapedPath(), nil)
	if err != nil {
		http.Error(w, "connectivity probe unavailable", http.StatusBadGateway)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if agent := r.UserAgent(); agent != "" {
		req.Header.Set("User-Agent", agent)
	}
	resp, err := s.transport.RoundTrip(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		http.Error(w, "connectivity probe failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, claudeProbeMaxReply+1))
	if err != nil || len(body) > claudeProbeMaxReply || resp.Header.Get("Content-Encoding") != "" {
		http.Error(w, "invalid connectivity probe reply", http.StatusBadGateway)
		return
	}
	// Do not follow redirects or copy cookies, locations or arbitrary headers.
	if contentType := resp.Header.Get("Content-Type"); len(contentType) < 1024 && !strings.ContainsAny(contentType, "\r\n") {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
