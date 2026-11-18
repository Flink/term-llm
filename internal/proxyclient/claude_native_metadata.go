package proxyclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	claudeNativeDiscoveryPath   = "/.well-known/term-llm/claude-proxy"
	claudeNativeMetadataLimit   = 64 << 10
	claudeNativeMetadataTTL     = 15 * time.Second
	claudeNativeAPIHost         = "api.anthropic.com"
	claudeNativePlatformHost    = "platform.claude.com"
	claudeNativeCAMaxBytes      = 32 << 10
	claudeNativeProtocolVersion = 2
	claudeNativeTokenMode       = "vault"
)

// claudeNativeHosts is the exact set of origins a v2 proxy CA may sign for.
// Inference uses the API host; native login/token exchange uses the platform
// host. Both must be intercepted or a login can leave the proxy boundary.
var claudeNativeHosts = []string{claudeNativeAPIHost, claudeNativePlatformHost}

var claudeNativeRoute = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// claudeNativeBootstrap is the validated public metadata returned by the proxy.
// It never contains vendor credentials or private key material.
type claudeNativeBootstrap struct {
	ProxyURL     string   // Exact binding-root origin without path or userinfo.
	Route        string   // Slug used as the Proxy-Authorization user name.
	CAPEM        []byte   // Canonical single-certificate PEM for the proxy CA.
	Protocol     int      // Protocol version advertised by the proxy.
	TokenMode    string   // Credential custody mode; v2 requires "vault".
	AllowedHosts []string // Exact names the CA and tunnel may cover.
}

type claudeNativeMetadata struct {
	ProtocolVersion int      `json:"protocol_version"`
	TokenMode       string   `json:"token_mode"`
	Hosts           []string `json:"hosts"`
	CertificatePEM  string   `json:"certificate_pem"`
	ProxyURL        string   `json:"proxy_url"`
	Route           string   `json:"route"`
}

// ValidateClaudeNativeToken reports whether a value is a scoped proxy client
// token rather than a vendor credential. Callers use it before any launch so a
// misconfigured binding fails closed instead of sending a real token.
func ValidateClaudeNativeToken(token string) error { return validateClaudeNativeToken(token) }

func validateClaudeNativeToken(token string) error {
	if token == "" || len(token) > 16<<10 || strings.TrimSpace(token) != token || strings.HasPrefix(token, "sk-ant-") || strings.HasPrefix(token, "admin_") {
		return errors.New("Claude proxy client token is missing or is a vendor/administrator credential")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == ':' {
			return errors.New("Claude proxy client token contains unsupported characters")
		}
	}
	return nil
}

// fetchClaudeNativeBootstrap consults only the binding root and never follows a
// redirect or an ambient process proxy. Errors deliberately omit the token, URL
// and response body so a failed bootstrap cannot disclose credentials.
func fetchClaudeNativeBootstrap(parent context.Context, root, token string) (*claudeNativeBootstrap, error) {
	canonical, err := canonicalRoot(root)
	if err != nil || !safeClaudeNativeRootPath(canonical) {
		return nil, errors.New("Claude proxy binding root must be an http(s) URL without credentials, query or fragment")
	}
	if err := validateClaudeNativeToken(token); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, claudeNativeMetadataTTL)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical+claudeNativeDiscoveryPath, nil)
	if err != nil {
		return nil, errors.New("Claude proxy metadata endpoint is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Term-LLM-Proxy-App", "term-llm")

	transport := &http.Transport{
		Proxy:                  nil, // A bootstrap URL must never inherit an ambient HTTP proxy.
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  claudeNativeMetadataTTL,
		MaxResponseHeaderBytes: claudeNativeMetadataLimit,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   claudeNativeMetadataTTL,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Claude proxy metadata request canceled or timed out: %w", ctx.Err())
		}
		return nil, errors.New("Claude proxy metadata request failed; check that the proxy is reachable (no direct fallback)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("Claude proxy metadata rejected the scoped client token (HTTP %d); check the route grant", resp.StatusCode)
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			return nil, fmt.Errorf("Claude proxy metadata returned HTTP %d; redirects are disabled", resp.StatusCode)
		default:
			return nil, fmt.Errorf("Claude proxy metadata returned HTTP %d; inspect the proxy route", resp.StatusCode)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, claudeNativeMetadataLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Claude proxy metadata request canceled or timed out: %w", ctx.Err())
		}
		return nil, errors.New("could not read Claude proxy metadata")
	}
	if len(body) > claudeNativeMetadataLimit {
		return nil, errors.New("Claude proxy metadata response exceeds 64 KiB")
	}
	var metadata claudeNativeMetadata
	if json.Unmarshal(body, &metadata) != nil {
		return nil, errors.New("Claude proxy metadata response is invalid")
	}
	if strings.TrimSpace(metadata.CertificatePEM) == "" || strings.TrimSpace(metadata.ProxyURL) == "" || strings.TrimSpace(metadata.Route) == "" {
		return nil, errors.New("Claude proxy metadata response is missing certificate_pem, proxy_url or route")
	}
	if metadata.ProtocolVersion != claudeNativeProtocolVersion {
		return nil, fmt.Errorf("Claude proxy metadata protocol_version %d is required; upgrade the proxy (no direct fallback)", claudeNativeProtocolVersion)
	}
	if metadata.TokenMode != claudeNativeTokenMode {
		return nil, errors.New(`Claude proxy metadata token_mode must be "vault"; the client refuses non-vault custody`)
	}
	if !claudeNativeExactHosts(metadata.Hosts) {
		return nil, errors.New("Claude proxy metadata hosts must be exactly api.anthropic.com and platform.claude.com")
	}
	proxyURL, err := validateClaudeNativeProxyURL(canonical, metadata.ProxyURL)
	if err != nil {
		return nil, err
	}
	if !claudeNativeRoute.MatchString(metadata.Route) {
		return nil, errors.New("Claude proxy metadata route must be a route slug")
	}
	caPEM, err := validateClaudeNativeCA(metadata.CertificatePEM)
	if err != nil {
		return nil, err
	}
	return &claudeNativeBootstrap{
		ProxyURL: proxyURL, Route: metadata.Route, CAPEM: caPEM,
		Protocol: metadata.ProtocolVersion, TokenMode: metadata.TokenMode,
		AllowedHosts: append([]string(nil), metadata.Hosts...),
	}, nil
}

// claudeNativeExactHosts requires the v2 host set with no duplicates or
// additions. Order is not part of the contract.
func claudeNativeExactHosts(hosts []string) bool {
	if len(hosts) != len(claudeNativeHosts) {
		return false
	}
	seen := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if seen[host] {
			return false
		}
		seen[host] = true
	}
	for _, want := range claudeNativeHosts {
		if !seen[want] {
			return false
		}
	}
	return true
}

func safeClaudeNativeRootPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return false
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.Contains(part, "\\") {
			return false
		}
	}
	return true
}

// validateClaudeNativeProxyURL requires the exact origin of the binding root
// with its route path removed. The scoped token is added only in memory later;
// the proxy must never advertise credentials, a path, query or fragment.
func validateClaudeNativeProxyURL(root, raw string) (string, error) {
	rootURL, err := url.Parse(root)
	if err != nil || rootURL.Host == "" {
		return "", errors.New("Claude proxy binding root is invalid")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Scheme == "" || u.Host == "" {
		return "", errors.New("Claude proxy metadata proxy_url must be the proxy origin without credentials, path, query or fragment")
	}
	if normalizeURL(u) != nil {
		return "", errors.New("Claude proxy metadata proxy_url is invalid")
	}
	if u.Scheme != rootURL.Scheme || u.Host != rootURL.Host {
		return "", errors.New("Claude proxy metadata proxy_url must be the exact binding root origin")
	}
	if u.Path != "" && u.Path != "/" || u.RawPath != "" && u.RawPath != "/" {
		return "", errors.New("Claude proxy metadata proxy_url must not contain the binding route path")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

// validateClaudeNativeCA accepts exactly one public CA certificate and requires
// the proxy's full constrained-CA shape: self-signed, server-auth-only, a
// zero-length path, and name constraints that cover exactly the Anthropic API
// and platform hosts. Private keys, extra certificates, headers and trailing
// data are rejected so metadata cannot substitute a broad trust anchor.
func validateClaudeNativeCA(raw string) ([]byte, error) {
	if len(raw) == 0 || len(raw) > claudeNativeCAMaxBytes {
		return nil, errors.New("Claude proxy certificate_pem must be one CA certificate")
	}
	rest := []byte(raw)
	var cert *x509.Certificate
	for {
		trimmed := bytes.TrimSpace(rest)
		if len(trimmed) == 0 {
			break
		}
		if cert != nil {
			return nil, errors.New("Claude proxy certificate_pem must contain exactly one CA certificate")
		}
		if !bytes.HasPrefix(trimmed, []byte("-----BEGIN ")) {
			return nil, errors.New("Claude proxy certificate_pem contains data outside a certificate")
		}
		block, remainder := pem.Decode(trimmed)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("Claude proxy certificate_pem must contain only a public certificate")
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("Claude proxy certificate_pem is not a valid X509 certificate")
		}
		cert = parsed
		rest = remainder
	}
	if cert == nil {
		return nil, errors.New("Claude proxy certificate_pem must be one CA certificate")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || !cert.MaxPathLenZero {
		return nil, errors.New("Claude proxy certificate_pem is not a constrained CA certificate")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("Claude proxy certificate_pem cannot sign server certificates")
	}
	if cert.CheckSignatureFrom(cert) != nil {
		return nil, errors.New("Claude proxy CA certificate is not self-signed")
	}
	if !cert.PermittedDNSDomainsCritical {
		return nil, errors.New("Claude proxy CA name constraints must be critical")
	}
	if !claudeNativeCAMatchesHosts(cert.PermittedDNSDomains, cert.ExcludedDNSDomains) {
		return nil, errors.New("Claude proxy CA name constraints must cover exactly the Anthropic API and platform hosts")
	}
	if len(cert.PermittedIPRanges) != 0 || !claudeNativeExcludesAllIPs(cert.ExcludedIPRanges) {
		return nil, errors.New("Claude proxy CA must exclude all IP address certificates")
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		return nil, errors.New("Claude proxy CA must be restricted to server authentication")
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Add(24*time.Hour).Before(cert.NotAfter) {
		return nil, errors.New("Claude proxy CA certificate is expired or has too little validity remaining")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

// claudeNativeCAMatchesHosts requires exactly the two v2 permitted DNS names
// and their subdomain exclusions, in any order. The certificate must also mark
// the constraints critical so a client cannot ignore them.
func claudeNativeCAMatchesHosts(permitted, excluded []string) bool {
	if len(permitted) != len(claudeNativeHosts) || len(excluded) != len(claudeNativeHosts) {
		return false
	}
	permittedSet := make(map[string]bool, len(permitted))
	for _, host := range permitted {
		permittedSet[host] = true
	}
	excludedSet := make(map[string]bool, len(excluded))
	for _, host := range excluded {
		excludedSet[host] = true
	}
	if len(permittedSet) != len(permitted) || len(excludedSet) != len(excluded) {
		return false
	}
	for _, host := range claudeNativeHosts {
		if !permittedSet[host] || !excludedSet["."+host] {
			return false
		}
	}
	return true
}

// claudeNativeExcludesAllIPs requires exactly the two zero-length exclusions
// the proxy advertises. DNS name constraints alone would still allow an
// arbitrary IP-address leaf to be signed by this process-local CA.
func claudeNativeExcludesAllIPs(ranges []*net.IPNet) bool {
	if len(ranges) != 2 {
		return false
	}
	var v4, v6 bool
	for _, network := range ranges {
		if network == nil {
			return false
		}
		ones, bits := network.Mask.Size()
		if ones != 0 {
			return false
		}
		switch bits {
		case 32:
			v4 = true
		case 128:
			v6 = true
		default:
			return false
		}
	}
	return v4 && v6
}
