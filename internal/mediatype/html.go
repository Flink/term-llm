package mediatype

import "io"

// HTMLFormat describes explicitly requested interactive documents. HTML is not
// part of the passive media allowlist and must never be inferred by Inspect.
func HTMLFormat() Format {
	return Format{MIME: "text/html", Extension: ".html", Kind: "html", MaxBytes: maxTextBytes}
}

// InspectHTML validates bounded UTF-8 text, not markup safety: scripts are
// intentional, and delivery must enforce a sandbox even on direct navigation.
func InspectHTML(r io.ReaderAt, size int64) (Format, error) {
	if size == 0 {
		return Format{}, ErrUnsupported
	}
	return inspectText(r, size, HTMLFormat())
}
