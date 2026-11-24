package mediatype

import (
	"bytes"
	"io"
	"unicode/utf8"
)

// HTMLFormat describes explicitly requested interactive documents. HTML is not
// part of the passive media allowlist and must never be inferred by Inspect.
func HTMLFormat() Format {
	return Format{MIME: "text/html", Extension: ".html", Kind: "html", MaxBytes: 4 << 20}
}

// InspectHTML validates bounded UTF-8 text, not markup safety: scripts are
// intentional, and delivery must enforce a sandbox even on direct navigation.
func InspectHTML(r io.ReaderAt, size int64) (Format, error) {
	f := HTMLFormat()
	if r == nil || size <= 0 || size > f.MaxBytes {
		return Format{}, ErrUnsupported
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, size), b); err != nil {
		return Format{}, err
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return Format{}, ErrUnsupported
	}
	return f, nil
}
