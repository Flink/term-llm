// Package debugws renders bounded previews of proxy WebSocket captures.
package debugws

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	debugWSOutputLimit   = 64 << 10
	debugWSMessageLimit  = 64 << 10
	debugWSTextReadLimit = 128 << 10
	debugWSFrameLimit    = 256
)

// Preview renders one direction of a raw RFC 6455 capture. Start
// at zero or at a cursor returned by this function; arbitrary offsets are not
// supported or validated as frame boundaries. masked must be true for the
// client direction and false for the server direction.
//
// A cursor less than total is the start of the next frame, not necessarily the
// start of a message. Fragment reassembly is local to a page; leading
// continuations on later pages are labeled but not decoded. Malformed or
// incomplete framing ends this snapshot's preview (nextOffset == total), with
// no scan for a new boundary. Do not reuse that end offset after capture grows.
//
// limit is an output byte budget, clamped to 64 KiB. Nonpositive limits disable
// preview. A page examines at most 256 frames, reading only their headers,
// close payloads (<=125 bytes each), and at most 128 KiB of text payload.
// Individual text messages larger than 64 KiB are not decoded.
func Preview(r io.ReaderAt, offset, total, limit int64, masked bool) (content string, nextOffset int64) {
	if limit <= 0 || offset < 0 || offset >= total {
		return "", max(0, total)
	}
	budget := int(min(limit, debugWSOutputLimit))
	var out strings.Builder
	p := debugWSPage{leading: offset > 0, textLeft: debugWSTextReadLimit}
	for n := 0; n < debugWSFrameLimit && offset < total && out.Len() < budget; n++ {
		f, issue := readDebugWSFrame(r, offset, total, masked)
		if issue != "" {
			return finishDebugWSPreview(out.String(), fmt.Sprintf("@%d %s\n", offset, issue), budget), total
		}
		hadFragment := p.fragmented
		if !hadFragment && f.opcode == 1 && !f.compressed && f.size <= debugWSMessageLimit && f.size > int64(p.textLeft) {
			break // This frame will fit the read budget on the next page.
		}
		detail, issue := p.previewFrame(r, f)
		if issue != "" {
			return finishDebugWSPreview(out.String(), fmt.Sprintf("@%d %s\n", offset, issue), budget), total
		}
		line := f.label(detail)
		if len(line) > budget-out.Len() {
			if out.Len() > 0 && !hadFragment {
				return out.String(), offset // Give this frame a fresh output budget.
			}
			return finishDebugWSPreview(out.String()+line, "[preview output truncated]\n", budget), f.end
		}
		out.WriteString(line)
		offset = f.end
	}
	if p.fragmented {
		note := "fragmented message spans preview pages; reassembly unavailable across pages"
		if offset == total {
			note = "truncated fragmented message: capture ended before final continuation"
		}
		return finishDebugWSPreview(out.String(), fmt.Sprintf("@%d %s\n", offset, note), budget), offset
	}
	return out.String(), offset
}

// Keep terminal diagnostics visible even if the transcript has already filled
// the page. Tiny limits may only allow a prefix of the diagnostic itself.
func finishDebugWSPreview(content, note string, limit int) string {
	if len(content)+len(note) <= limit {
		return content + note
	}
	if len(note) >= limit {
		return clipDebugWSUTF8(note, limit)
	}
	return clipDebugWSUTF8(content, limit-len(note)-1) + "\n" + note
}

func clipDebugWSUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

type debugWSFrame struct {
	start, payload, end, size int64
	opcode                    byte
	fin, compressed, masked   bool
	key                       [4]byte
}

func readDebugWSFrame(r io.ReaderAt, offset, total int64, masked bool) (f debugWSFrame, issue string) {
	var header [2]byte
	if !readDebugWSAt(r, header[:], offset, total) {
		return f, "truncated or unreadable frame header"
	}
	f.start = offset
	f.fin, f.compressed = header[0]&0x80 != 0, header[0]&0x40 != 0
	f.opcode, f.masked = header[0]&0xf, header[1]&0x80 != 0
	if f.masked != masked {
		return f, "malformed WebSocket frame: unexpected masking for capture direction"
	}
	if f.kind() == "" {
		return f, "malformed WebSocket frame: reserved opcode"
	}
	// Recognize RSV1 on the first data frame as a compression marker only.
	// No extension metadata is available to interpret other reserved bits.
	if header[0]&0x30 != 0 || (f.compressed && (f.opcode == 0 || f.opcode >= 8)) {
		return f, "malformed WebSocket frame: unsupported reserved bits (RSV)"
	}
	code := header[1] & 0x7f
	if f.opcode >= 8 && (!f.fin || code > 125) {
		return f, "malformed WebSocket frame: fragmented or oversized control frame"
	}
	length, width, issue := readDebugWSLength(r, offset+2, total, code)
	if issue != "" {
		return f, issue
	}
	f.payload = offset + 2 + width
	if f.masked {
		if !readDebugWSAt(r, f.key[:], f.payload, total) {
			return f, "truncated or unreadable frame masking key"
		}
		f.payload += int64(len(f.key))
	}
	f.size = length
	if length > total-f.payload {
		return f, fmt.Sprintf("truncated %s frame payload: declared %d bytes, only %d captured", f.kind(), length, total-f.payload)
	}
	f.end = f.payload + length
	return f, ""
}

func readDebugWSLength(r io.ReaderAt, offset, total int64, code byte) (length, width int64, issue string) {
	var ext [8]byte
	switch code {
	case 126:
		if !readDebugWSAt(r, ext[:2], offset, total) {
			return 0, 0, "truncated or unreadable frame extended length"
		}
		if n := binary.BigEndian.Uint16(ext[:2]); n >= 126 {
			return int64(n), 2, ""
		}
	case 127:
		if !readDebugWSAt(r, ext[:], offset, total) {
			return 0, 0, "truncated or unreadable frame extended length"
		}
		if n := binary.BigEndian.Uint64(ext[:]); n >= 65536 && n>>63 == 0 {
			return int64(n), 8, ""
		}
	default:
		return int64(code), 0, ""
	}
	return 0, 0, "malformed WebSocket frame: invalid or non-minimal extended length"
}

func readDebugWSAt(r io.ReaderAt, dst []byte, offset, total int64) bool {
	if int64(len(dst)) > total-offset {
		return false
	}
	if len(dst) == 0 {
		return true
	}
	n, _ := r.ReadAt(dst, offset)
	return n == len(dst)
}

func (f debugWSFrame) readPayload(r io.ReaderAt, dst []byte) bool {
	if !readDebugWSAt(r, dst, f.payload, f.end) {
		return false
	}
	if f.masked {
		for i := range dst {
			dst[i] ^= f.key[i%len(f.key)]
		}
	}
	return true
}

func (f debugWSFrame) kind() string {
	switch f.opcode {
	case 0:
		return "continuation"
	case 1:
		return "text"
	case 2:
		return "binary"
	case 8:
		return "close"
	case 9:
		return "ping"
	case 10:
		return "pong"
	default:
		return ""
	}
}

func (f debugWSFrame) label(detail string) string {
	flags := "fragment"
	if f.fin {
		flags = "FIN"
	}
	if f.masked {
		flags += ", masked"
	}
	line := fmt.Sprintf("[%d..%d) %s (opcode 0x%x, %s, %d payload bytes)\n", f.start, f.end, f.kind(), f.opcode, flags, f.size)
	if detail != "" {
		line += detail + "\n"
	}
	return line
}

type debugWSPage struct {
	fragmented, leading bool
	messageStart        int64
	textLeft            int
	text                []byte
	skip                string
}

func (p *debugWSPage) previewFrame(r io.ReaderAt, f debugWSFrame) (detail, issue string) {
	if f.opcode >= 8 {
		return previewDebugWSControl(r, f)
	}
	if f.opcode == 0 {
		if !p.fragmented {
			if !p.leading {
				return "", "malformed WebSocket frame: unexpected continuation"
			}
			p.skip = "message start unavailable on this page; continuation payload not decoded"
		}
	} else {
		if p.fragmented {
			return "", "malformed WebSocket frame: new data frame before final continuation"
		}
		p.messageStart = f.start
		switch {
		case f.compressed:
			p.skip = "compressed (RSV1) message; payload not decoded"
		case f.opcode == 2:
			p.skip = "binary message; payload not decoded"
		}
	}
	p.leading = false
	p.fragmented = !f.fin
	if p.skip == "" {
		detail, issue = p.previewText(r, f)
	} else {
		detail = p.skip
	}
	if f.fin {
		p.text = nil
		p.skip = ""
	}
	return detail, issue
}

func (p *debugWSPage) previewText(r io.ReaderAt, f debugWSFrame) (detail, issue string) {
	if f.size > int64(debugWSMessageLimit-len(p.text)) || f.size > int64(p.textLeft) {
		p.text = nil
		p.skip = "text message exceeds preview byte budget; payload not decoded"
		return p.skip, ""
	}
	start := len(p.text)
	p.text = append(p.text, make([]byte, int(f.size))...)
	if !f.readPayload(r, p.text[start:]) {
		return "", "truncated or unreadable text frame payload"
	}
	p.textLeft -= int(f.size)
	if !f.fin {
		return "", ""
	}
	if !utf8.Valid(p.text) {
		return "", "malformed WebSocket message: text payload is not UTF-8"
	}
	text := formatDebugWSText(p.text)
	if f.opcode == 0 {
		text = fmt.Sprintf("reassembled text from @%d:\n%s", p.messageStart, text)
	}
	return text, ""
}

func previewDebugWSControl(r io.ReaderAt, f debugWSFrame) (detail, issue string) {
	if f.size == 0 {
		return "", ""
	}
	if f.opcode != 8 {
		return "opaque control payload; not decoded", ""
	}
	if f.size == 1 {
		return "", "malformed WebSocket close frame: one-byte status"
	}
	var payload [125]byte
	if !f.readPayload(r, payload[:f.size]) {
		return "", "truncated or unreadable close frame payload"
	}
	code := binary.BigEndian.Uint16(payload[:2])
	if !validDebugWSCloseCode(code) {
		return "", fmt.Sprintf("malformed WebSocket close frame: invalid status %d", code)
	}
	reason := payload[2:f.size]
	if !utf8.Valid(reason) {
		return "", "malformed WebSocket close frame: reason is not UTF-8"
	}
	if len(reason) == 0 {
		return fmt.Sprintf("status %d", code), ""
	}
	return fmt.Sprintf("status %d: %s", code, readableDebugWSText(reason)), ""
}

func validDebugWSCloseCode(code uint16) bool {
	switch code {
	case 1004, 1005, 1006:
		return false
	default:
		return (code >= 1000 && code <= 1014) || (code >= 3000 && code <= 4999)
	}
}

func formatDebugWSText(data []byte) string {
	// Bound the input and nesting before json.Indent: indentation can amplify
	// otherwise small, deeply nested JSON. Larger inputs remain readable text.
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && len(trimmed) <= 8<<10 && (trimmed[0] == '{' || trimmed[0] == '[') && shallowDebugWSJSON(trimmed) {
		var pretty bytes.Buffer
		if json.Indent(&pretty, trimmed, "", "  ") == nil {
			return readableDebugWSText(pretty.Bytes())
		}
	}
	return readableDebugWSText(data)
}

func shallowDebugWSJSON(data []byte) bool {
	var quoted, escaped bool
	depth := 0
	for _, b := range data {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 16 {
				return false
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !quoted
}

// Keep line breaks and tabs, but show nonprinting characters (including
// terminal escapes and directional controls) literally instead of emitting them.
func readableDebugWSText(data []byte) string {
	var text strings.Builder
	for _, r := range string(data) {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			text.WriteRune(r)
		} else {
			quoted := strconv.QuoteRune(r)
			text.WriteString(quoted[1 : len(quoted)-1])
		}
	}
	return text.String()
}
