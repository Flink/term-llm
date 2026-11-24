package share

import "github.com/term-llm/term-llm/internal/mediatype"

const OmittedHTMLPreview = "Interactive HTML preview (not included in export)"

// OmittedDocumentNote describes why a published document is absent from an export.
// The MIME must be normalized. Other media return an empty note.
func OmittedDocumentNote(mime string) string {
	switch mime {
	case "text/html":
		return OmittedHTMLPreview
	case mediatype.MarkdownMIME, mediatype.PlainTextMIME:
		return "Text document (not included in export)"
	default:
		return ""
	}
}
