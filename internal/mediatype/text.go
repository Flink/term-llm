package mediatype

import (
	"bytes"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LookupDocument finds an explicitly published document, not a binary media format.
func LookupDocument(mime string) (Format, bool) {
	for _, f := range []Format{HTMLFormat(), markdownFormat, plainTextFormat} {
		if strings.EqualFold(strings.TrimSpace(mime), f.MIME) {
			return f, true
		}
	}
	return Format{}, false
}

// DocumentForExtension opts a local filename into HTML or Markdown rendering.
// Plain text is detected from content instead, regardless of suffix.
func DocumentForExtension(ext string) (Format, bool) {
	switch strings.ToLower(ext) {
	case ".html", ".htm":
		return HTMLFormat(), true
	case ".md", ".markdown":
		return markdownFormat, true
	default:
		return Format{}, false
	}
}

// InspectMarkdown validates explicitly requested Markdown as passive UTF-8 text.
func InspectMarkdown(r io.ReaderAt, size int64) (Format, error) {
	return inspectText(r, size, markdownFormat)
}

func inspectText(r io.ReaderAt, size int64, f Format) (Format, error) {
	if r == nil || size < 0 || size > f.MaxBytes {
		return Format{}, ErrUnsupported
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, size), b); err != nil {
		return Format{}, err
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return Format{}, ErrUnsupported
	}
	if f.Kind == Text && hasNonTextControl(b) {
		return Format{}, ErrUnsupported
	}
	return f, nil
}

func hasNonTextControl(b []byte) bool {
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n' && r != '\f' {
			return true
		}
		b = b[n:]
	}
	return false
}
