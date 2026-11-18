package usagetap

import "bytes"

// missingContentType is used only when an upstream omits its media type. The
// endpoint still selects the schema; a bounded response prefix selects framing.
const missingContentType Format = "missing-content-type"
const maxFramingPrefix = 64

// detectFraming retains at most a small prefix, including across split reads.
// Explicit unsupported media types never enter this fallback. It observes bytes
// only: neither forwarded headers nor the body are changed.
func (p *Parser) detectFraming(data []byte) {
	for len(data) > 0 && len(p.buf) < maxFramingPrefix {
		p.buf = append(p.buf, data[0])
		data = data[1:]
		prefix := bytes.TrimSpace(p.buf)
		if len(prefix) == 0 {
			continue
		}
		format, pending := framingPrefix(prefix)
		if pending {
			continue
		}
		buffered := p.buf
		p.buf = nil
		p.format = format
		if format == "" {
			p.MarkUnknown()
			return
		}
		p.Feed(buffered)
		p.Feed(data)
		return
	}
	if len(p.buf) == maxFramingPrefix {
		p.MarkUnknown()
		p.format = ""
		p.buf = nil
	}
}

func framingPrefix(prefix []byte) (Format, bool) {
	if prefix[0] == '{' || prefix[0] == '[' {
		return JSON, false
	}
	for _, field := range []string{"event:", "data:", ":", "id:", "retry:"} {
		if bytes.HasPrefix(prefix, []byte(field)) {
			return SSE, false
		}
		if bytes.HasPrefix([]byte(field), prefix) {
			return "", true
		}
	}
	return "", false
}
