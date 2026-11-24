package session

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/share"
)

func TestShareBundleOmitsPublishedHTML(t *testing.T) {
	const ref = "0123456789abcdef0123456789abcdef"
	const scriptMarker = "PRIVATE_PREVIEW_SCRIPT"
	const document = "<!doctype html><script>console.log('" + scriptMarker + "')</script>"
	dir := t.TempDir()
	item := core.MediaArtifact{
		Reference: ref, Name: "preview.html", MediaType: "text/html",
		SourcePath: filepath.Join(dir, "preview.html"),
		StoredPath: filepath.Join(dir, "fedcba9876543210fedcba9876543210.html"),
	}
	for _, path := range []string{item.SourcePath, item.StoredPath} {
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const uri = "term-llm-media://" + ref
	const example = "![Example](" + uri + ")"
	const answer = "![Live preview](" + uri + ")\n\n```\n" + example + "\n```"
	call := &core.ToolCall{ID: "c1", Name: "show_media", Arguments: []byte(`{"path":"preview.html"}`)}
	result := &core.ToolResult{ID: call.ID, Name: call.Name, Content: "Media ready (text/html).", Media: []core.MediaArtifact{item}}
	messages := []Message{
		{ID: 1, Sequence: 1, Role: core.RoleAssistant, ResponseID: "r1", Parts: []core.Part{{Type: core.PartToolCall, ToolCall: call}}},
		{ID: 2, Sequence: 2, Role: core.RoleTool, ResponseID: "r1", Parts: []core.Part{{Type: core.PartToolResult, ToolResult: result}}},
		{ID: 3, Sequence: 3, Role: core.RoleAssistant, ResponseID: "r1", TextContent: answer, Parts: []core.Part{{Type: core.PartText, Text: answer}}},
	}
	for _, scope := range []ShareScope{ShareScopeConversation, ShareScopeResponse} {
		t.Run(string(scope), func(t *testing.T) {
			selection, err := SelectShare(messages, 3, scope)
			if err != nil {
				t.Fatal(err)
			}
			files, err := ShareBundle(&Session{ID: "demo"}, selection.Messages, ExportOptions{
				ResponseOnly: scope == ShareScopeResponse, Media: selection.Media,
				Images: ExportImagesAssets, AssetMediaTypes: []string{"image/png"}, IncludeAudio: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			byName := bundleByName(files)
			if len(files) != 2 || byName["session.md"].Content == nil || byName["index.html"].Content == nil {
				for _, f := range files {
					t.Log(f.Name)
				}
				t.Fatal("expected only the two transcripts")
			}
			for _, f := range files {
				body := string(f.Content)
				fenced := "```\n" + example + "\n```"
				if f.Name == "index.html" {
					fenced = "<pre><code>" + example + "\n</code></pre>"
				}
				if strings.Count(body, share.OmittedHTMLPreview) != 1 || strings.Count(body, ref) != 1 || !strings.Contains(body, fenced) {
					t.Errorf("%s: expected a placeholder for the live preview and only the literal example to retain its URI", f.Name)
				}
				for _, leak := range []string{
					scriptMarker, base64.StdEncoding.EncodeToString([]byte(document)),
					item.SourcePath, filepath.Base(item.StoredPath), "data:text/html", "<iframe",
					`href="preview.html"`, `src="preview.html"`, "](preview.html)",
				} {
					if strings.Contains(body, leak) {
						t.Errorf("%s exposed %q", f.Name, leak)
					}
				}
			}
		})
	}
}

func TestShareBundleOmitsTextDocuments(t *testing.T) {
	const ref = "0123456789abcdef0123456789abcdef"
	const marker = "PRIVATE_DOCUMENT_BYTES"
	for _, mime := range []string{"text/markdown", "text/plain"} {
		t.Run(mime, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.whatever")
			if err := os.WriteFile(path, []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			item := core.MediaArtifact{Reference: ref, StoredPath: path, MediaType: mime}
			answer := "![Document](term-llm-media://" + ref + ")"
			messages := []Message{{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartText, Text: answer}}}}
			files, err := ShareBundle(&Session{ID: "demo"}, messages, ExportOptions{Media: []core.MediaArtifact{item}, Images: ExportImagesAssets, AssetMediaTypes: []string{"image/png", mime}})
			if err != nil || len(files) != 2 {
				t.Fatalf("bundle has %d files, err = %v", len(files), err)
			}
			for _, file := range files {
				body := string(file.Content)
				if !strings.Contains(body, "Text document (not included in export)") || strings.Contains(body, marker) || strings.Contains(body, ref) || strings.Contains(body, path) {
					t.Errorf("%s did not safely omit the text artifact", file.Name)
				}
			}
		})
	}
}
