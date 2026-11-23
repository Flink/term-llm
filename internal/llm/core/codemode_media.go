package core

import (
	"iter"
	"strings"
)

// CodeModeMediaURLPrefix starts the opaque URL returned by show_media.
const CodeModeMediaURLPrefix = "term-llm-media://"

// CodeModeMediaURLs locates complete opaque media URLs in emitted text. The
// offsets are byte offsets. A URL match is not itself a media grant.
func CodeModeMediaURLs(text string) iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		for scan := 0; scan < len(text); {
			i := strings.Index(text[scan:], CodeModeMediaURLPrefix)
			if i < 0 {
				return
			}
			start := scan + i
			scan = start + len(CodeModeMediaURLPrefix)
			end := scan + 32 // show_media uses 16 random bytes, hex encoded.
			if end > len(text) || !mediaURLStart(text, start) || !mediaURLEnd(text, end) {
				continue
			}
			ref := text[scan:end]
			if strings.IndexFunc(ref, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) == -1 && !yield(start, text[start:end]) {
				return
			}
		}
	}
}

func mediaURLStart(text string, start int) bool {
	// JSON text emissions can contain escaped whitespace before a URL.
	return start == 0 || !mediaURLContinuation(text[start-1]) ||
		(start >= 2 && text[start-2] == '\\' && strings.ContainsRune("nrt", rune(text[start-1])))
}

func mediaURLEnd(text string, end int) bool {
	// Permit a sentence-ending period, but not a suffix like .png.
	if end < len(text) && text[end] == '.' {
		end++
	}
	return end == len(text) || !mediaURLContinuation(text[end])
}

func mediaURLContinuation(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~:/?#@%&+", rune(b))
}
