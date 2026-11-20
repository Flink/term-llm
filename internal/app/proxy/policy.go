package proxy

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// validatePolicy checks syntax only: account existence and origin scope are
// validated by the trusted account integration before activation/dispatch.
func validatePolicy(r *Route) error {
	if r.Account != "" && !slug.MatchString(r.Account) {
		return fmt.Errorf("invalid account name")
	}
	if r.Redirects != "" && r.Redirects != "passthrough" && r.Redirects != "refuse" {
		return fmt.Errorf("redirects must be passthrough or refuse")
	}
	if r.Usage != "" && r.Usage != "off" && r.Usage != "auto" {
		return fmt.Errorf("usage must be off or auto")
	}
	if len(r.Allow) > 128 {
		return fmt.Errorf("at most 128 allow rules")
	}
	seen := map[string]bool{}
	for _, rule := range r.Allow {
		method, p, ok := strings.Cut(rule, " ")
		if r.Account != "" && strings.HasSuffix(p, "/*") && !strings.HasPrefix(p, "=") {
			return fmt.Errorf("catch-all allow rules are not permitted on account routes")
		}
		if !ok || !httpguts.ValidHeaderFieldName(method) || method != strings.ToUpper(method) || method == "CONNECT" ||
			strings.ContainsRune(method, '*') || !validAllowPath(p) || seen[rule] {
			return fmt.Errorf("allow rules must be unique canonical METHOD /prefix, METHOD =/path or METHOD /prefix/* entries")
		}
		seen[rule] = true
	}
	// Rule order has no meaning and must not cause needless regrants.
	r.Allow = append([]string(nil), r.Allow...)
	sort.Strings(r.Allow)
	return nil
}

func validAllowPath(p string) bool {
	if exact, ok := strings.CutPrefix(p, "="); ok {
		p = exact
	} else if strings.HasSuffix(p, "/*") {
		p = strings.TrimSuffix(p, "*")
	}
	return !strings.ContainsRune(p, '*') && canonicalPolicyPath(p)
}

func canonicalPolicyPath(p string) bool {
	return safePath(p) && !strings.ContainsAny(p, "%?# ") && (path.Clean(p) == p || p != "/" && path.Clean(p)+"/" == p)
}

// Policy paths are relative to the upstream base, not the local mount. Exact
// and plain entries require canonical paths; a catch-all permits a safe opaque tail.
func policyAllows(r Route, method, p string) bool {
	if len(r.Allow) == 0 {
		return true
	}
	plainMatch := false
	for _, entry := range r.Allow {
		if !allowEntryMatches(entry, method, p) {
			continue
		}
		if strings.HasSuffix(entry, "/*") {
			return safePath(p)
		}
		plainMatch = true
	}
	return plainMatch && canonicalPolicyPath(p)
}

// Match literally against the escaped path. The slash before a terminal * is
// part of the prefix, unlike plain entries which have no segment boundary.
// Callers enforce canonical/safe path checks or Bedrock's fixed model shape.
func allowEntryMatches(entry, method, path string) bool {
	m, prefix, ok := strings.Cut(entry, " ")
	if !ok || m != method {
		return false
	}
	if exact, ok := strings.CutPrefix(prefix, "="); ok {
		return path == exact
	}
	if strings.HasSuffix(prefix, "/*") {
		prefix = strings.TrimSuffix(prefix, "*")
	}
	return strings.HasPrefix(path, prefix)
}

// ResponseBodyTap is an optional response-only observer seam. It must preserve
// bytes/backpressure and leave resp.Body with the same Close ownership. For 101 it
// must also preserve io.ReadWriteCloser and support concurrent Close/Read/Write.
// Installed before serving requests.
type ResponseBodyTap func(Route, *http.Response, *Record)
