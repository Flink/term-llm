package session

import (
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestInteractiveHTMLIsNotIncludedInExports(t *testing.T) {
	const ref = "0123456789abcdef0123456789abcdef"
	item := core.MediaArtifact{Reference: ref, MediaType: "text/html", SourcePath: "/private/filter.html", Name: "filter.html"}
	opts := ExportOptions{Media: []core.MediaArtifact{item}, IncludeAudio: true}
	text := "Try this:\n\n![Filter example](term-llm-media://" + ref + ")"
	msgs := []Message{{Role: core.RoleAssistant, TextContent: text, Parts: []core.Part{{Type: core.PartText, Text: text}}}}
	sess := &Session{ID: "demo"}
	html, err := ExportToHTML(sess, msgs, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{html, ExportToMarkdown(sess, msgs, opts), replaceHTMLMediaReferences(text, map[string]core.MediaArtifact{ref: item}, newExportImages(opts))} {
		if !strings.Contains(output, omittedHTMLPreview) || strings.Contains(output, ref) || strings.Contains(output, item.SourcePath) || strings.Contains(output, "<iframe") {
			t.Fatal("expected text-only HTML preview placeholder")
		}
	}
}
