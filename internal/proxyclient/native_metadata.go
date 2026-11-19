package proxyclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	claudeNativeDiscoveryPath = "/.well-known/term-llm/claude-proxy"
	agyNativeDiscoveryPath    = "/.well-known/term-llm/agy-proxy"
	nativeMetadataLimit       = 64 << 10
	nativeMetadataTTL         = 15 * time.Second
	claudeNativeAPIHost       = "api.anthropic.com"
	claudeNativePlatformHost  = "platform.claude.com"
	nativeCAMaxBytes          = 32 << 10
	nativeProtocolVersion     = 2
	nativeTokenMode           = "vault"
)

// claudeNativeHosts is the exact set of origins a v2 proxy CA may sign for.
// Inference uses the API host; native login/token exchange uses the platform
// host. Both must be intercepted or a login can leave the proxy boundary.
var claudeNativeHosts = []string{claudeNativeAPIHost, claudeNativePlatformHost}

var nativeRoute = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// nativeBootstrap is the validated metadata returned by the proxy. It may
// carry scoped aliases, but never vendor credentials or private key material.
type nativeBootstrap struct {
	ProxyURL     string   // Exact binding-root origin without path or userinfo.
	Route        string   // Slug used as the Proxy-Authorization user name.
	CAPEM        []byte   // Canonical single-certificate PEM for the proxy CA.
	Protocol     int      // Protocol version advertised by the proxy.
	TokenMode    string   // Credential custody mode; v2 requires "vault".
	AllowedHosts []string // Exact names the CA and tunnel may cover.
	claudeAccess claudeNativeAccess
	agyIdentity  agySharedIdentity
}

type nativeMetadata struct {
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
	return validateNativeToken("Claude", token, "sk-ant-")
}

func validateNativeToken(label, token, vendorPrefix string) error {
	if token == "" || len(token) > 16<<10 || strings.TrimSpace(token) != token || strings.HasPrefix(token, vendorPrefix) || strings.HasPrefix(token, "admin_") || strings.HasPrefix(token, "sk-tlpadm-") {
		return fmt.Errorf("%s proxy client token is missing or is a vendor/administrator credential", label)
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == ':' {
			return fmt.Errorf("%s proxy client token contains unsupported characters", label)
		}
	}
	return nil
}

type nativePolicy struct {
	label, discovery, hostDescription string
	hosts                             []string
	validateToken                     func(string) error
}

var claudeNativePolicy = nativePolicy{
	label: "Claude", discovery: claudeNativeDiscoveryPath, hosts: claudeNativeHosts,
	hostDescription: "api.anthropic.com and platform.claude.com", validateToken: ValidateClaudeNativeToken,
}

func (p nativePolicy) errorf(format string, args ...any) error {
	return fmt.Errorf(p.label+" proxy "+format, args...)
}

// fetchBootstrap consults only the binding root and never follows a
// redirect or an ambient process proxy. Errors deliberately omit the token, URL
// and response body so a failed bootstrap cannot disclose credentials.
func (p nativePolicy) fetchBootstrap(parent context.Context, root, token string) (*nativeBootstrap, error) {
	canonical, err := canonicalRoot(root)
	if err != nil || !safeNativeRootPath(canonical) {
		return nil, p.errorf("binding root must be an http(s) URL without credentials, query or fragment")
	}
	if err := p.validateToken(token); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, nativeMetadataTTL)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical+p.discovery, nil)
	if err != nil {
		return nil, p.errorf("metadata endpoint is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Term-LLM-Proxy-App", "term-llm")

	transport := &http.Transport{
		Proxy:                  nil, // A bootstrap URL must never inherit an ambient HTTP proxy.
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  nativeMetadataTTL,
		MaxResponseHeaderBytes: nativeMetadataLimit,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   nativeMetadataTTL,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, p.errorf("metadata request canceled or timed out: %w", ctx.Err())
		}
		return nil, p.errorf("metadata request failed; check that the proxy is reachable (no direct fallback)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, p.errorf("metadata rejected the scoped client token (HTTP %d); check the route grant", resp.StatusCode)
		case resp.StatusCode == http.StatusConflict && (p.discovery == claudeNativeDiscoveryPath || p.discovery == agyNativeDiscoveryPath):
			return nil, p.errorf("shared subscription is not connected (HTTP 409); ask the proxy operator to sign in")
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			return nil, p.errorf("metadata returned HTTP %d; redirects are disabled", resp.StatusCode)
		default:
			return nil, p.errorf("metadata returned HTTP %d; inspect the proxy route", resp.StatusCode)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, nativeMetadataLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, p.errorf("metadata request canceled or timed out: %w", ctx.Err())
		}
		return nil, p.errorf("metadata response could not be read")
	}
	if len(body) > nativeMetadataLimit {
		return nil, p.errorf("metadata response exceeds 64 KiB")
	}
	return p.parseBootstrap(canonical, body)
}

func (p nativePolicy) parseBootstrap(canonical string, body []byte) (*nativeBootstrap, error) {
	var metadata nativeMetadata
	if json.Unmarshal(body, &metadata) != nil {
		return nil, p.errorf("metadata response is invalid")
	}
	if strings.TrimSpace(metadata.CertificatePEM) == "" || strings.TrimSpace(metadata.ProxyURL) == "" || strings.TrimSpace(metadata.Route) == "" {
		return nil, p.errorf("metadata response is missing certificate_pem, proxy_url or route")
	}
	if metadata.ProtocolVersion != nativeProtocolVersion {
		return nil, p.errorf("metadata protocol_version %d is required; upgrade the proxy (no direct fallback)", nativeProtocolVersion)
	}
	if metadata.TokenMode != nativeTokenMode {
		return nil, p.errorf(`metadata token_mode must be "vault"; the client refuses non-vault custody`)
	}
	if !exactNativeHosts(metadata.Hosts, p.hosts) {
		return nil, p.errorf("metadata hosts must be exactly %s", p.hostDescription)
	}
	proxyURL, err := p.validateProxyURL(canonical, metadata.ProxyURL)
	if err != nil {
		return nil, err
	}
	if !nativeRoute.MatchString(metadata.Route) {
		return nil, p.errorf("metadata route must be a route slug")
	}
	caPEM, err := p.validateCA(metadata.CertificatePEM)
	if err != nil {
		return nil, err
	}
	bootstrap := &nativeBootstrap{
		ProxyURL: proxyURL, Route: metadata.Route, CAPEM: caPEM,
		Protocol: metadata.ProtocolVersion, TokenMode: metadata.TokenMode,
		AllowedHosts: append([]string(nil), metadata.Hosts...),
	}
	switch p.discovery {
	case claudeNativeDiscoveryPath:
		bootstrap.claudeAccess, err = parseClaudeNativeAccess(body)
	case agyNativeDiscoveryPath:
		bootstrap.agyIdentity, err = parseAgySharedIdentity(body)
	}
	if err != nil {
		return nil, err
	}
	return bootstrap, nil
}

// exactNativeHosts requires the v2 host set with no duplicates or
// additions. Order is not part of the contract.
func exactNativeHosts(hosts, expected []string) bool {
	if len(hosts) != len(expected) {
		return false
	}
	seen := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if seen[host] {
			return false
		}
		seen[host] = true
	}
	for _, want := range expected {
		if !seen[want] {
			return false
		}
	}
	return true
}

func safeNativeRootPath(raw string) bool {
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

// validateProxyURL requires the exact origin of the binding root
// with its route path removed. The scoped token is added only in memory later;
// the proxy must never advertise credentials, a path, query or fragment.
func (p nativePolicy) validateProxyURL(root, raw string) (string, error) {
	rootURL, err := url.Parse(root)
	if err != nil || rootURL.Host == "" {
		return "", p.errorf("binding root is invalid")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Scheme == "" || u.Host == "" {
		return "", p.errorf("metadata proxy_url must be the proxy origin without credentials, path, query or fragment")
	}
	if normalizeURL(u) != nil {
		return "", p.errorf("metadata proxy_url is invalid")
	}
	if u.Scheme != rootURL.Scheme || u.Host != rootURL.Host {
		return "", p.errorf("metadata proxy_url must be the exact binding root origin")
	}
	if u.Path != "" && u.Path != "/" || u.RawPath != "" && u.RawPath != "/" {
		return "", p.errorf("metadata proxy_url must not contain the binding route path")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

// validateCA accepts exactly one public CA certificate and requires
// the proxy's full constrained-CA shape: self-signed, server-auth-only, a
// zero-length path, and name constraints covering only the provider hosts. Private keys, extra certificates, headers and trailing
// data are rejected so metadata cannot substitute a broad trust anchor.
func (p nativePolicy) validateCA(raw string) ([]byte, error) {
	if len(raw) == 0 || len(raw) > nativeCAMaxBytes {
		return nil, p.errorf("certificate_pem must be one CA certificate")
	}
	rest := []byte(raw)
	var cert *x509.Certificate
	for {
		trimmed := bytes.TrimSpace(rest)
		if len(trimmed) == 0 {
			break
		}
		if cert != nil {
			return nil, p.errorf("certificate_pem must contain exactly one CA certificate")
		}
		if !bytes.HasPrefix(trimmed, []byte("-----BEGIN ")) {
			return nil, p.errorf("certificate_pem contains data outside a certificate")
		}
		block, remainder := pem.Decode(trimmed)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, p.errorf("certificate_pem must contain only a public certificate")
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, p.errorf("certificate_pem is not a valid X509 certificate")
		}
		cert = parsed
		rest = remainder
	}
	if cert == nil {
		return nil, p.errorf("certificate_pem must be one CA certificate")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || !cert.MaxPathLenZero {
		return nil, p.errorf("certificate_pem is not a constrained CA certificate")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, p.errorf("certificate_pem cannot sign server certificates")
	}
	if cert.CheckSignatureFrom(cert) != nil {
		return nil, p.errorf("CA certificate is not self-signed")
	}
	if !cert.PermittedDNSDomainsCritical {
		return nil, p.errorf("CA name constraints must be critical")
	}
	if !nativeCAMatchesHosts(cert.PermittedDNSDomains, cert.ExcludedDNSDomains, p.hosts) {
		return nil, p.errorf("CA name constraints must cover exactly %s", p.hostDescription)
	}
	if len(cert.PermittedIPRanges) != 0 || !nativeExcludesAllIPs(cert.ExcludedIPRanges) {
		return nil, p.errorf("CA must exclude all IP address certificates")
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		return nil, p.errorf("CA must be restricted to server authentication")
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Add(24*time.Hour).Before(cert.NotAfter) {
		return nil, p.errorf("CA certificate is expired or has too little validity remaining")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

// nativeCAMatchesHosts requires exactly the v2 permitted DNS names
// and their subdomain exclusions, in any order. The certificate must also mark
// the constraints critical so a client cannot ignore them.
func nativeCAMatchesHosts(permitted, excluded, expected []string) bool {
	if len(permitted) != len(expected) || len(excluded) != len(expected) {
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
	for _, host := range expected {
		if !permittedSet[host] || !excludedSet["."+host] {
			return false
		}
	}
	return true
}

// nativeExcludesAllIPs requires exactly the two zero-length exclusions
// the proxy advertises. DNS name constraints alone would still allow an
// arbitrary IP-address leaf to be signed by this process-local CA.
func nativeExcludesAllIPs(ranges []*net.IPNet) bool {
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

func (b *nativeBootstrap) connectURL(token string) string {
	u, _ := url.Parse(b.ProxyURL)
	u.User = url.UserPassword(b.Route, token)
	return u.String()
}
