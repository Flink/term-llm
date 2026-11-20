package proxy

import (
	"encoding/hex"
	"net/url"
	"strings"
)

func safePath(escaped string) bool {
	if len(escaped) > 8<<10 || !strings.HasPrefix(escaped, "/") {
		return false
	}
	// Check every layer, including the wire path. Bound the number of passes
	// and reject if the path is still changing at the last pass.
	p := escaped
	for i := 0; i < 4; i++ {
		for _, c := range p {
			if c == '\\' || c < 32 || c == 127 {
				return false
			}
		}
		for s := range strings.SplitSeq(p, "/") {
			if s == "." || s == ".." {
				return false
			}
		}
		var q string
		if i == 0 {
			var err error
			q, err = url.PathUnescape(p)
			if err != nil {
				return false
			}
		} else {
			q = unescapeNestedPath(p)
		}
		if q == p {
			return true
		}
		p = q
	}
	return false
}

// After the wire pass, a percent that doesn't start a valid triplet is literal.
// It must not prevent decoding the valid triplets elsewhere in the path.
func unescapeNestedPath(p string) string {
	var b strings.Builder
	last := 0
	for i := 0; i+2 < len(p); i++ {
		if p[i] != '%' {
			continue
		}
		var decoded [1]byte
		if _, err := hex.Decode(decoded[:], []byte(p[i+1:i+3])); err != nil {
			continue
		}
		b.WriteString(p[last:i])
		b.WriteByte(decoded[0])
		i += 2
		last = i + 1
	}
	if last == 0 {
		return p
	}
	b.WriteString(p[last:])
	return b.String()
}
