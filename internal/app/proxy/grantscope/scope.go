package grantscope

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

type HeaderRule struct {
	Header, Secret, Prefix string
	Value                  *string
}

type Scope struct {
	Rules     []HeaderRule
	Account   string
	WebSocket bool
	Allow     []string
}

func IsSRV(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "srv:")
}

// Destination uses the exact SRV reference for discovered destinations and
// retains the original fingerprint encoding for static HTTP(S) destinations.
func (s Scope) Destination(upstream, origin string, target *url.URL) string {
	if IsSRV(upstream) {
		return s.hash(upstream, "")
	}
	return s.hash(origin, canonicalBase(target))
}

// LegacySRV recognizes only the previous DNS-independent SRV scope, never a
// grant tied to a resolved host (including the old trusted-account SRV scope).
func (s Scope) LegacySRV(upstream string) string {
	if s.Account == "" && IsSRV(upstream) {
		if u, err := url.Parse(strings.TrimSpace(upstream)); err == nil && u.Scheme == "srv" {
			return s.hash("srv://"+strings.ToLower(u.Host), canonicalBase(u))
		}
	}
	return ""
}

func (s Scope) hash(origin, base string) string {
	sort.Slice(s.Rules, func(i, j int) bool { return s.Rules[i].Header < s.Rules[j].Header })
	return fingerprint(struct {
		Origin, Base string
		Rules        []HeaderRule
		Account      string   `json:",omitempty"`
		WebSocket    bool     `json:",omitempty"`
		Allow        []string `json:",omitempty"`
	}{origin, base, s.Rules, s.Account, s.WebSocket, s.Allow})
}

func canonicalBase(u *url.URL) string {
	// Escape hex digits are case-insensitive, but encoded separators remain encoded.
	encoded := []byte(strings.TrimSuffix(u.EscapedPath(), "/") + "/")
	for i := 0; i+2 < len(encoded); i++ {
		if encoded[i] == '%' {
			encoded[i+1], encoded[i+2] = upperHex(encoded[i+1]), upperHex(encoded[i+2])
			i += 2
		}
	}
	return string(encoded)
}

func upperHex(b byte) byte {
	if b >= 'a' && b <= 'f' {
		return b - 'a' + 'A'
	}
	return b
}

type Rule struct{ PathPrefix, Authority string }

// Composite binds child authorities to their subpaths and redirect policy.
func Composite(rules []Rule, redirects string) string {
	sort.Slice(rules, func(i, j int) bool { return rules[i].PathPrefix < rules[j].PathPrefix })
	if redirects == "" {
		redirects = "passthrough"
	}
	return fingerprint(struct {
		Rules     []Rule
		Redirects string
	}{rules, redirects})
}

func fingerprint(scope any) string {
	b, _ := json.Marshal(scope)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
