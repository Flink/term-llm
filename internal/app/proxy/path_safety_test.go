package proxy

import (
	"strings"
	"testing"
)

func TestSafePathDecoding(t *testing.T) {
	for _, path := range []string{
		"/v1/%25z/%252e%252e/x", "/v1/100%25/%255c/x", "/v1/%25z/%2500/x",
		"/v1/%2e/x", "/v1/%25%32%66..%25%32%66/x", "/v1/%z/x", "/v1/%",
		"/v1/%25252541", "/" + strings.Repeat("x", 8192),
	} {
		if safePath(path) {
			t.Errorf("accepted unsafe path of %d bytes: %.50q", len(path), path)
		}
	}
	for _, path := range []string{
		"/v1/id%2Fpart", "/v1/100%25/%2541", "/v1/%25z/%252541", "/v1/end%25", "/" + strings.Repeat("x", 8191),
	} {
		if !safePath(path) {
			t.Errorf("rejected safe path of %d bytes: %.50q", len(path), path)
		}
	}
}
