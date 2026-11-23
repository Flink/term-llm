package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
	corestream "github.com/term-llm/term-llm/internal/llm/core/stream"
)

func TestCodeModeImagesExplicitBoundedAndCellScoped(t *testing.T) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	e := newCodeEngine()
	cell := admitTestCell(t, e)
	handle, err := cell.saveImage(base64.StdEncoding.EncodeToString(buffer.Bytes()), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(map[string]any{"type": "image", "handle": handle})
	part, err := cell.Image(value)
	if err != nil || part.ImageData.Width != 2 || part.ImageData.Height != 3 {
		t.Fatalf("%+v %v", part, err)
	}
	if _, err = cell.Image(json.RawMessage(`"forged"`)); err == nil {
		t.Fatal("forged image accepted")
	}
	if _, err = cell.Image(json.RawMessage(`{"type":"image","url":"http://127.0.0.1/secret"}`)); err == nil {
		t.Fatal("URL accepted as image authority")
	}
	cell.Close()
	second, err := e.admitCodeCell(context.Background(), "second", corestream.Sender{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err = second.Image(value); err == nil {
		t.Fatal("cross-cell handle accepted")
	}
}

const testCodeMediaRef = "0123456789abcdef0123456789abcdef"

func testCodeMedia() core.MediaArtifact {
	return core.MediaArtifact{
		Reference: testCodeMediaRef, SourcePath: "/private/source.png", StoredPath: "/private/published.png",
		MediaType: "image/png", Name: "figure.png", Caption: "figure",
	}
}

func TestCodeModeMediaEmissionsAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		want       bool
	}{
		{"text", `text(r.text); text(r.text)`, true},
		{"uppercase reference", `text(r.text.replace(/[a-f0-9]{32}/, ref => ref.toUpperCase()))`, true},
		{"return object", `return r`, true},
		{"not emitted", `return 'done'`, false},
		{"state only", `store('private', r); return 'done'`, false},
		{"error only", `throw Error(r.text)`, false},
		{"image argument", `image(r.text)`, false},
		{"partial URL", `text(r.text.slice(0, -1))`, false},
		{"longer URL", `text(r.text + '/other')`, false},
		{"forged URL and path", `text('term-llm-media://' + 'a'.repeat(32) + ' /private/ungranted.png')`, false},
		{"text before failure", `text(r.text); throw Error('failed later')`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCodeEngine()
			t.Cleanup(e.CloseCodeMode)
			artifact := testCodeMedia()
			url := core.CodeModeMediaURLPrefix + artifact.Reference
			if err := e.RegisterTool(codeTestTool{name: "show_media", run: func(context.Context, json.RawMessage) (core.ToolOutput, error) {
				return core.ToolOutput{Content: "ready\n" + url, Media: []core.MediaArtifact{artifact}}, nil
			}}); err != nil {
				t.Fatal(err)
			}
			ctx := asyncCodeContext("media", "outer")
			ch := make(chan core.Event, 64)
			result, err := e.codeModeSessions.exec(ctx, `const r = await tools.show_media({}); `+tc.code, 3*time.Second, 0, corestream.NewSender(ctx, ch))
			if err != nil || !isCodeModeTerminal(result.Status) {
				t.Fatalf("expected a terminal result: %+v, %v", result, err)
			}
			if tc.want {
				if !slices.Equal(result.Media, []core.MediaArtifact{artifact}) || !strings.Contains(strings.ToLower(partsText(result)), url) {
					t.Fatalf("missing attachment for emitted URL: %+v", result)
				}
			} else if len(result.Media) > 0 {
				t.Fatalf("non-emission granted media: %+v", result.Media)
			}
			if strings.Contains(partsText(result), artifact.Path()) || strings.Contains(partsText(result), artifact.SourcePath) {
				t.Fatal("local paths leaked to JavaScript output")
			}
			var nestedEnd bool
			for len(ch) > 0 {
				ev := <-ch
				if ev.Type == core.EventToolExecEnd && ev.ParentToolCallID == "outer" {
					nestedEnd = true
					if ev.ToolOutput != "" || len(ev.ToolMedia)+len(ev.ToolImages)+len(ev.ToolContentParts) > 0 {
						t.Fatalf("nested result was not redacted: %+v", ev)
					}
				}
			}
			if !nestedEnd {
				t.Fatal("missing nested tool end event")
			}
			if tc.name == "state only" {
				loaded, err := e.ExecCodeMode(ctx, `return load('private')`, 3*time.Second, 0)
				if err != nil || !strings.Contains(partsText(loaded), url) || strings.Contains(partsText(loaded), "/private/") || len(loaded.Media) > 0 {
					t.Fatalf("state carried more than the original JS text: %+v, %v", loaded, err)
				}
			}
		})
	}
}

func TestCodeModeMediaWaitPagination(t *testing.T) {
	e := newCodeEngine()
	t.Cleanup(e.CloseCodeMode)
	artifact := testCodeMedia()
	url := core.CodeModeMediaURLPrefix + artifact.Reference
	if err := e.RegisterTool(codeTestTool{name: "show_media", run: func(context.Context, json.RawMessage) (core.ToolOutput, error) {
		return core.ToolOutput{Content: url, Media: []core.MediaArtifact{artifact}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 1), make(chan struct{})
	if err := e.RegisterTool(codeModeBlockingTool(started, release)); err != nil {
		t.Fatal(err)
	}
	ctx := asyncCodeContext("tiny-pages", "spawn")
	first, err := e.ExecCodeMode(ctx, `const r = await tools.show_media({}); text('abc ' + r.text); await tools.hold({}); return 'done';`, 3*time.Second, 5)
	if err != nil || first.Status != "running" || partsText(first) != "abc " || len(first.Media) != 0 || !first.HasMoreOutput || !first.OutputLimited {
		t.Fatalf("first page should end before the URL: %+v, %v", first, err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("nested hold did not start")
	}
	close(release)
	e.codeModeSessions.lookup(first.CellID, "tiny-pages").awaitDone(ctx, 3*time.Second)
	small, err := e.WaitCodeMode(ctx, first.CellID, 0, 1)
	if err != nil || small.Status != "completed" || partsText(small) != "" || len(small.Media) != 0 || !small.HasMoreOutput || !small.OutputLimited || small.OutputTruncated {
		t.Fatalf("a tiny page should defer the complete URL without dropping output: %+v, %v", small, err)
	}
	const bigger = 20
	final, err := e.WaitCodeMode(ctx, first.CellID, 3*time.Second, bigger)
	if err != nil || final.Status != "completed" || partsText(final) != url+"\ndone" || !slices.Equal(final.Media, []core.MediaArtifact{artifact}) || final.HasMoreOutput {
		t.Fatalf("larger wait did not deliver the URL with its attachment: %+v, %v", final, err)
	}
	again, err := e.WaitCodeMode(ctx, first.CellID, 0, bigger)
	if err != nil || len(again.Parts)+len(again.Media) != 0 {
		t.Fatalf("drained output replayed: %+v, %v", again, err)
	}
}

func TestCodeModeMediaClippedByTailRetention(t *testing.T) {
	artifact := testCodeMedia()
	url := core.CodeModeMediaURLPrefix + artifact.Reference
	capability := &nestedCell{}
	capability.rememberMedia(core.ToolOutput{Content: url, Media: []core.MediaArtifact{artifact}})
	cell := newCodeModeAsyncCell(nil, "test", "tail", capability)
	// Clip the opening of the first URL, leaving it and a second URL in the tail.
	text := url + " " + strings.Repeat("x", codemode.SessionLimits.RetainBytes-len(url)-4) + " " + url
	emitCodeModeText(t, cell, text)
	parts, media, limited := cell.drainPartsLocked(len(url))
	if !limited || len(media) != 0 || !cell.truncated || cell.omittedBytes <= 0 || len(parts) == 0 {
		t.Fatalf("expected clipped prefix without a grant: %d parts, %+v, limited=%t", len(parts), media, limited)
	}
	_, media, limited = cell.drainPartsLocked(codemode.SessionLimits.RetainBytes)
	if limited || !slices.Equal(media, []core.MediaArtifact{artifact}) {
		t.Fatalf("complete URL was lost in retained tail: %+v, limited=%t", media, limited)
	}
}
