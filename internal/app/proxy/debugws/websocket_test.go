package debugws

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func syntheticDebugWSFrame(flags byte, masked bool, payload []byte) []byte {
	h := []byte{flags, 0}
	switch n := len(payload); {
	case n < 126:
		h[1] = byte(n)
	case n < 65536:
		h[1] = 126
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	default:
		h[1] = 127
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	if masked {
		h[1] |= 0x80
		key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
		h = append(h, key[:]...)
		for i, b := range payload {
			h = append(h, b^key[i%len(key)])
		}
		return h
	}
	return append(h, payload...)
}

func assertDebugWSContains(t *testing.T, content string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(content, s) {
			t.Errorf("missing %q in %q", s, content)
		}
	}
}

func TestDecodeDebugWebSocketMaskedFragmentedJSON(t *testing.T) {
	message := []byte(`{"demo":"☃","items":[1,2]}`)
	cut := bytes.Index(message, []byte("☃")) + 1 // split inside a UTF-8 rune
	first := syntheticDebugWSFrame(0x01, true, message[:cut])
	ping := syntheticDebugWSFrame(0x89, true, []byte{0, 1, 2})
	middle := syntheticDebugWSFrame(0x00, true, message[cut:cut+2])
	pong := syntheticDebugWSFrame(0x8a, true, nil)
	last := syntheticDebugWSFrame(0x80, true, message[cut+2:])
	closeFrame := syntheticDebugWSFrame(0x88, true, append([]byte{0x03, 0xe8}, "done"...))
	wire := bytes.Join([][]byte{first, ping, middle, pong, last, closeFrame}, nil)
	content, next := Preview(bytes.NewReader(wire), 0, int64(len(wire)), 4096, true)
	if next != int64(len(wire)) {
		t.Fatalf("next = %d, want end %d", next, len(wire))
	}
	assertDebugWSContains(t, content,
		fmt.Sprintf("[0..%d) text (opcode 0x1, fragment, masked", len(first)),
		fmt.Sprintf("[%d..%d) ping (opcode 0x9", len(first), len(first)+len(ping)),
		"continuation (opcode 0x0", "pong (opcode 0xa", "close (opcode 0x8", "status 1000: done",
		"reassembled text from @0:\n{\n  \"demo\": \"☃\",\n  \"items\": [\n    1,\n    2\n  ]\n}")
	if strings.Count(content, "☃") != 1 || !utf8.ValidString(content) {
		t.Fatalf("text must appear exactly once as UTF-8: %q", content)
	}
}

func TestDecodeDebugWebSocketExtendedLengths(t *testing.T) {
	for _, n := range []int{125, 126, 65535, 65536} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			wire := syntheticDebugWSFrame(0x81, true, bytes.Repeat([]byte{'z'}, n))
			content, next := Preview(bytes.NewReader(wire), 0, int64(len(wire)), 256, true)
			if next != int64(len(wire)) {
				t.Fatalf("next = %d, want end %d", next, len(wire))
			}
			assertDebugWSContains(t, content, fmt.Sprintf("[0..%d) text", len(wire)), fmt.Sprintf("%d payload bytes", n), "zzzzzzzz")
			if n > 126 {
				assertDebugWSContains(t, content, "preview output truncated")
			}
		})
	}
}

func TestDecodeDebugWebSocketOpaqueMessages(t *testing.T) {
	wire := bytes.Join([][]byte{
		syntheticDebugWSFrame(0x82, false, []byte("not-displayed")),
		syntheticDebugWSFrame(0x41, false, []byte("also-hidden")),
		syntheticDebugWSFrame(0x89, false, []byte("opaque")),
		syntheticDebugWSFrame(0x80, false, []byte("still-hidden")),
		syntheticDebugWSFrame(0x81, false, []byte("readable 🍀\nline\t\x1b\u202e")),
	}, nil)
	content, next := Preview(bytes.NewReader(wire), 0, int64(len(wire)), 4096, false)
	if next != int64(len(wire)) {
		t.Fatalf("next = %d, want end %d", next, len(wire))
	}
	assertDebugWSContains(t, content, "binary message; payload not decoded", "opaque control payload; not decoded", "readable 🍀\nline\t\\x1b\\u202e")
	if strings.Count(content, "compressed (RSV1) message; payload not decoded") != 2 || strings.Contains(content, "hidden") || strings.Contains(content, "displayed") || strings.ContainsAny(content, "\x1b\u202e") {
		t.Fatalf("unexpected opaque or control content: %q", content)
	}
}

func TestDecodeDebugWebSocketStopsOnMalformed(t *testing.T) {
	tests := []struct {
		name string
		bad  []byte
		want string
	}{
		{"direction", syntheticDebugWSFrame(0x81, true, []byte("wrong-direction")), "unexpected masking"},
		{"reserved opcode", []byte{0x83, 0}, "reserved opcode"},
		{"reserved bit", []byte{0xa1, 0}, "unsupported reserved bits"},
		{"compressed continuation", []byte{0xc0, 0}, "unsupported reserved bits"},
		{"fragmented control", []byte{0x09, 0}, "fragmented or oversized control frame"},
		{"oversized control", []byte{0x89, 126, 0, 126}, "fragmented or oversized control frame"},
		{"nonminimal 16-bit length", []byte{0x81, 126, 0, 125}, "non-minimal extended length"},
		{"nonminimal 64-bit length", []byte{0x81, 127, 0, 0, 0, 0, 0, 0, 255, 255}, "non-minimal extended length"},
		{"length high bit", []byte{0x81, 127, 128, 0, 0, 0, 0, 0, 0, 0}, "invalid or non-minimal extended length"},
		{"unexpected continuation", []byte{0x80, 0}, "unexpected continuation"},
		{"missing continuation", []byte{0x01, 0, 0x81, 0}, "new data frame before final continuation"},
		{"invalid text", []byte{0x81, 1, 0xff}, "text payload is not UTF-8"},
		{"invalid close size", []byte{0x88, 1, 0}, "one-byte status"},
		{"invalid close code", []byte{0x88, 2, 0x03, 0xed}, "invalid status 1005"},
		{"invalid close reason", []byte{0x88, 3, 0x03, 0xe8, 0xff}, "reason is not UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			good := syntheticDebugWSFrame(0x81, false, []byte("before"))
			later := syntheticDebugWSFrame(0x81, false, []byte("must not be read"))
			wire := bytes.Join([][]byte{good, tt.bad, later}, nil)
			r := &countingDebugWSReader{ReaderAt: bytes.NewReader(wire)}
			content, next := Preview(r, 0, int64(len(wire)), 4096, false)
			assertDebugWSContains(t, content, "before", "malformed WebSocket", tt.want)
			if next != int64(len(wire)) || r.furthest > int64(len(good)+len(tt.bad)) {
				t.Fatalf("decoder resynchronized after malformed frame: next = %d, reads up to %d", next, r.furthest)
			}
		})
	}
}

func TestDecodeDebugWebSocketTruncated(t *testing.T) {
	tests := []struct {
		name   string
		wire   []byte
		masked bool
		want   string
	}{
		{"base header", []byte{0x81}, false, "truncated or unreadable frame header"},
		{"length", []byte{0x81, 126, 0}, false, "truncated or unreadable frame extended length"},
		{"64-bit length", []byte{0x81, 127, 0, 0, 0, 0, 0, 2}, false, "truncated or unreadable frame extended length"},
		{"mask", []byte{0x81, 0x80, 0, 0, 0}, true, "truncated or unreadable frame masking key"},
		{"payload", []byte{0x81, 4, 'a'}, false, "truncated text frame payload: declared 4 bytes, only 1 captured"},
		{"unfinished message", []byte{0x01, 1, 'a'}, false, "capture ended before final continuation"},
		{"huge declared payload", []byte{0x81, 127, 127, 255, 255, 255, 255, 255, 255, 255}, false, "declared 9223372036854775807 bytes, only 0 captured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, base := range []int64{0, math.MaxInt64 - int64(len(tt.wire))} {
				total := base + int64(len(tt.wire))
				r := debugWSReaderFunc(func(p []byte, offset int64) (int, error) {
					if offset < base || int64(len(p)) > total-offset {
						t.Fatalf("read outside capture: offset %d length %d", offset, len(p))
					}
					return copy(p, tt.wire[offset-base:]), nil
				})
				content, next := Preview(r, base, total, 4096, tt.masked)
				assertDebugWSContains(t, content, tt.want)
				if next != total {
					t.Fatalf("next = %d, want end %d", next, total)
				}
			}
		})
	}
}

func TestDecodeDebugWebSocketReaderShorterThanSnapshot(t *testing.T) {
	r := bytes.NewReader([]byte{0x81, 3, 'x'})
	content, next := Preview(r, 0, 5, 4096, false)
	assertDebugWSContains(t, content, "truncated or unreadable text frame payload")
	if next != 5 {
		t.Fatalf("next = %d, want end 5", next)
	}
}

type debugWSReaderFunc func([]byte, int64) (int, error)

func (fn debugWSReaderFunc) ReadAt(p []byte, offset int64) (int, error) { return fn(p, offset) }

type countingDebugWSReader struct {
	io.ReaderAt
	requested, furthest int64
}

func (r *countingDebugWSReader) ReadAt(p []byte, offset int64) (int, error) {
	r.requested += int64(len(p))
	r.furthest = max(r.furthest, offset+int64(len(p)))
	return r.ReaderAt.ReadAt(p, offset)
}

func TestDecodeDebugWebSocketSkipsOversizedByWireLength(t *testing.T) {
	const size = int64(1 << 40)
	header := binary.BigEndian.AppendUint64([]byte{0x81, 127}, uint64(size))
	following := syntheticDebugWSFrame(0x81, false, []byte("after oversized"))
	boundary := int64(len(header)) + size
	total := boundary + int64(len(following))
	r := debugWSReaderFunc(func(p []byte, offset int64) (int, error) {
		switch {
		case offset >= 0 && offset < int64(len(header)) && int64(len(p)) <= int64(len(header))-offset:
			return copy(p, header[offset:]), nil
		case offset >= boundary && int64(len(p)) <= total-offset:
			return copy(p, following[offset-boundary:]), nil
		default:
			t.Fatalf("oversized payload read at offset %d, size %d", offset, len(p))
			return 0, io.EOF
		}
	})
	content, next := Preview(r, 0, total, 4096, false)
	if next != total {
		t.Fatalf("next = %d, want end %d", next, total)
	}
	assertDebugWSContains(t, content, "exceeds preview byte budget; payload not decoded", fmt.Sprintf("[%d..%d) text", boundary, total), "after oversized")
}

func TestDecodeDebugWebSocketFragmentExceedsMessageCap(t *testing.T) {
	first := syntheticDebugWSFrame(0x01, false, bytes.Repeat([]byte{'a'}, debugWSMessageLimit))
	last := syntheticDebugWSFrame(0x80, false, []byte("not read"))
	after := syntheticDebugWSFrame(0x81, false, []byte("after the message"))
	wire := bytes.Join([][]byte{first, last, after}, nil)
	r := &countingDebugWSReader{ReaderAt: bytes.NewReader(wire)}
	content, next := Preview(r, 0, int64(len(wire)), 4096, false)
	assertDebugWSContains(t, content, "continuation", "text message exceeds preview byte budget", "after the message")
	if next != int64(len(wire)) || r.requested != int64(len(wire)-len("not read")) || strings.Contains(content, "aaaa") {
		t.Fatalf("skipped fragment incorrectly: next = %d, requested = %d", next, r.requested)
	}
}

func TestDecodeDebugWebSocketFrameBoundaryPaging(t *testing.T) {
	first := syntheticDebugWSFrame(0x81, false, []byte("one"))
	second := syntheticDebugWSFrame(0x81, false, []byte("two"))
	wire := append(first, second...)
	r := bytes.NewReader(wire)
	total := int64(len(wire))
	content, next := Preview(r, 0, total, 70, false)
	assertDebugWSContains(t, content, "[0..5) text", "one")
	if next != int64(len(first)) || strings.Contains(content, "two") {
		t.Fatalf("first page next = %d, content %q", next, content)
	}
	content, next = Preview(r, next, total, 70, false)
	assertDebugWSContains(t, content, "[5..10) text", "two")
	if next != total {
		t.Fatalf("next = %d, want end %d", next, total)
	}
	if content, next := Preview(r, total, total, 70, false); content != "" || next != total {
		t.Fatalf("end must be stable: next = %d, content %q", next, content)
	}
}

func TestDecodeDebugWebSocketFrameCapInsideFragment(t *testing.T) {
	first := syntheticDebugWSFrame(0x01, false, []byte("begin "))
	ping := syntheticDebugWSFrame(0x89, false, nil)
	prefix := append(first, bytes.Repeat(ping, debugWSFrameLimit-1)...)
	boundary := int64(len(prefix))
	last := syntheticDebugWSFrame(0x80, false, []byte("end of message"))
	later := syntheticDebugWSFrame(0x81, false, []byte("later message"))
	wire := bytes.Join([][]byte{prefix, ping, last, later}, nil)
	r := &countingDebugWSReader{ReaderAt: bytes.NewReader(wire)}
	total := int64(len(wire))
	content, next := Preview(r, 0, total, debugWSOutputLimit, false)
	assertDebugWSContains(t, content, "fragmented message spans preview pages")
	if next != boundary || r.furthest != boundary {
		t.Fatalf("unexpected work at frame cap: next = %d, reads up to %d, boundary %d", next, r.furthest, boundary)
	}
	content, next = Preview(r, next, total, debugWSOutputLimit, false)
	assertDebugWSContains(t, content, "ping", "message start unavailable on this page", "later message")
	if next != total || strings.Contains(content, "end of message") {
		t.Fatalf("second page next = %d, content %q", next, content)
	}
}

func TestDecodeDebugWebSocketCapsTextRead(t *testing.T) {
	// Formatting trims padding so the output budget doesn't mask the read cap.
	first := syntheticDebugWSFrame(0x81, false, append(bytes.Repeat([]byte{' '}, debugWSMessageLimit-2), '{', '}'))
	wire := bytes.Repeat(first, 3)
	r := &countingDebugWSReader{ReaderAt: bytes.NewReader(wire)}
	total := int64(len(wire))
	content, next := Preview(r, 0, total, debugWSOutputLimit, false)
	if next != 2*int64(len(first)) || strings.Count(content, "{}") != 2 || r.requested != debugWSTextReadLimit+30 {
		t.Fatalf("text-read cap not respected: next = %d, requested = %d", next, r.requested)
	}
	content, next = Preview(r, next, total, debugWSOutputLimit, false)
	if next != total || strings.Count(content, "{}") != 1 {
		t.Fatalf("failed to resume after read cap: next = %d, content %q", next, content)
	}
}

func TestDecodeDebugWebSocketCapsOutputAtUTF8Boundary(t *testing.T) {
	wire := syntheticDebugWSFrame(0x81, false, bytes.Repeat([]byte("☃\x1b"), 16384))
	total := int64(len(wire))
	for _, limit := range []int64{1, 89, 90, 91, debugWSOutputLimit * 4} {
		content, next := Preview(bytes.NewReader(wire), 0, total, limit, false)
		if next != total || len(content) == 0 || int64(len(content)) > min(limit, debugWSOutputLimit) || !utf8.ValidString(content) {
			t.Fatalf("limit = %d: next = %d, content length = %d, valid UTF-8 = %t", limit, next, len(content), utf8.ValidString(content))
		}
		if limit >= 89 && !strings.Contains(content, "preview output truncated") {
			t.Fatalf("limit = %d: no truncation marker", limit)
		}
	}
}

func TestDecodeDebugWebSocketBoundedJSONFormatting(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64),
		`{"not":"valid",}`,
	} {
		wire := syntheticDebugWSFrame(0x81, false, []byte(text))
		content, next := Preview(bytes.NewReader(wire), 0, int64(len(wire)), 4096, false)
		assertDebugWSContains(t, content, "\n"+text+"\n")
		if next != int64(len(wire)) {
			t.Fatalf("next = %d, want end %d", next, len(wire))
		}
	}
}
