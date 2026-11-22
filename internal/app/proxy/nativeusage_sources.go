package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage"
	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage/poll"
)

type nativeUsageReport = nativeusage.Report

type nativeUsageSource = poll.Source

type nativeUsageBinding struct {
	route     activeRoute
	kind, key string
	apiKey    string
	identity  string
}

var nativeUsageMACKey = sync.OnceValue(func() []byte { return []byte(randomToken("native-usage-")) })

func nativeUsageKey(parts ...string) string {
	mac := hmac.New(sha256.New, nativeUsageMACKey())
	_ = json.NewEncoder(mac).Encode(parts)
	return hex.EncodeToString(mac.Sum(nil))
}

func nativeUsageIdentity(a accountRecord) string {
	return nativeUsageKey(a.Name, a.Kind, fmt.Sprint(a.identityVersion), a.credentials.AccountID, a.claudeSharedID)
}

// nativeUsageSources takes a read-only inventory. It does not open a vault,
// read client credentials, consult worker grants, or send upstream requests.
func (s *Service) nativeUsageSources() []nativeUsageSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nativeUsageSourcesLocked()
}

func (s *Service) nativeUsageSourcesLocked() []nativeUsageSource {
	snap := s.snap
	if s.closing || snap == nil {
		return nil
	}
	byProvider := map[string][]nativeUsageBinding{}
	accounts := map[string]accountRecord{}
	for _, route := range snap.routes {
		if !route.Enabled {
			continue
		}
		for _, dest := range route.destinations() {
			a, seen := accounts[dest.Account]
			if dest.Account != "" && !seen {
				a, _ = s.store.account(dest.Account)
				accounts[dest.Account] = a
			}
			if b, ok := usageBinding(dest, a); ok {
				byProvider[route.Provider] = append(byProvider[route.Provider], b)
			}
		}
	}
	out := make([]nativeUsageSource, 0, len(byProvider))
	for _, id := range slices.Sorted(maps.Keys(byProvider)) {
		bindings := byProvider[id]
		seen := map[string]bool{}
		start := len(out)
		for _, b := range bindings {
			if seen[b.key] {
				continue
			}
			seen[b.key] = true
			out = append(out, nativeUsageSource{ID: id, Key: b.key, Route: b.route.Name + strings.TrimSuffix(b.route.pathPrefix, "/"), Fetch: func(ctx context.Context) (nativeUsageReport, error) {
				return s.fetchNativeUsageBinding(ctx, snap, b)
			}})
		}
		if len(out) == start+1 {
			out[start].Route = ""
		}
	}
	return out
}

func usageBinding(r activeRoute, a accountRecord) (nativeUsageBinding, bool) {
	b := nativeUsageBinding{route: r}
	if r.target == nil || r.target.RawQuery != "" || r.target.ForceQuery || r.Billing == "local" {
		return b, false
	}
	root := strings.TrimSuffix(r.target.EscapedPath(), "/")
	if r.Account != "" {
		if !usableNativeUsageAccount(a, r.origin) {
			return b, false
		}
		switch a.Kind {
		case "chatgpt":
			if r.origin != "https://chatgpt.com:443" || root != "/backend-api" && root != "/backend-api/codex" {
				return b, false
			}
		case "grok":
			if r.origin != "https://cli-chat-proxy.grok.com:443" || root != "/v1" {
				return b, false
			}
		case "claude-bin", "agy-bin":
			p := nativeProviderForKind(a.Kind)
			if r.origin != "https://"+p.apiHost+":443" || root != "" {
				return b, false
			}
		default:
			return b, false
		}
		b.kind, b.identity = a.Kind, nativeUsageIdentity(a)
		b.key = b.identity
		return b, true
	}
	// A static key must have been installed on this exact vendor destination.
	// A CallerAuth credential is incoming only, never a vendor key.
	b.kind, b.apiKey = nativeUsageStaticKey(r, root)
	if b.apiKey == "" {
		return b, false
	}
	b.key = nativeUsageKey(b.kind, b.apiKey)
	return b, true
}

func usableNativeUsageAccount(a accountRecord, origin string) bool {
	if a.State != "ready" && a.State != "refreshing" || !accountOriginAllowed(a, origin) || a.endpoint != "" && a.endpoint != origin {
		return false
	}
	if nativeProviderForKind(a.Kind) != nil {
		return a.claudeShared() && len(a.claudeSharedCipher) != 0
	}
	// Never let quota polling drive an OAuth exchange via an operator fixture
	// override. Requests with the defaults still go through custody refresh.
	return len(a.operator.Origins) == 0 && a.operator.Issuer == "" && a.operator.ClientID == "" && a.credentials.AccessToken != "" && a.credentials.AccountID != ""
}

func nativeUsageStaticKey(r activeRoute, root string) (kind, key string) {
	switch r.origin {
	case "https://opencode.ai:443":
		switch root {
		case "/zen/go/v1":
			key = nativeUsageBearer(r.headers)
		case "/zen/go":
			key = r.headers.Get("X-Api-Key")
		case "/inference/go/v1":
			key = r.headers.Get("X-Goog-Api-Key")
		}
		return "opencode-go", strings.TrimSpace(key)
	case "https://openrouter.ai:443":
		if root == "/api/v1" {
			return "openrouter", nativeUsageBearer(r.headers)
		}
	}
	return "", ""
}

func nativeUsageBearer(headers http.Header) string {
	scheme, secret, ok := strings.Cut(headers.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(secret)
}

var errNativeUsageChanged = errors.New("native usage route or account changed")

// A config snapshot can change during a quota read. An edit to unrelated
// settings does not revoke a binding; removing its last eligible route does.
func (s *Service) nativeUsageCurrent(snap *snapshot, b nativeUsageBinding) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closing || s.snap == nil {
		return false
	}
	if s.snap == snap {
		return true
	}
	var a accountRecord
	if b.identity != "" {
		a, _ = s.store.account(b.route.Account)
	}
	for _, r := range s.snap.routes {
		if !r.Enabled || r.Provider != b.route.Provider || r.Account != b.route.Account {
			continue
		}
		for _, dest := range r.destinations() {
			if current, ok := usageBinding(dest, a); ok && current.key == b.key {
				return true
			}
		}
	}
	return false
}
