package ui

import (
	"context"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestStreamAdapterForwardsOnlyScriptReceipts(t *testing.T) {
	for _, name := range []string{"spawn_script", "wait_script", "cancel_script", "code_mode", "shell"} {
		t.Run(name, func(t *testing.T) {
			const receipt = `{"cell_id":"cell-1","status":"yielded","state_committed":false,"max_wait":0}`
			parts := []core.ToolContentPart{
				{Type: core.ToolContentPartText, Text: "visible output"},
				{Type: core.ToolContentPartText, Text: "\n" + receipt},
			}
			end := core.Event{Type: core.EventToolExecEnd, ToolCallID: "call-1", ToolName: name, ToolSuccess: true, ToolOutput: receipt, ToolContentParts: parts}
			// Receipt forwarding must retain the existing deduplication even if no
			// start event was observed (as with a detached/re-attached run).
			adapter := NewStreamAdapter(10)
			go adapter.ProcessStream(context.Background(), &testStream{events: []core.Event{end, end}})
			var ends []StreamEvent
			for event := range adapter.Events() {
				if event.Type == StreamEventToolEnd {
					ends = append(ends, event)
				}
			}
			if len(ends) != 1 {
				t.Fatalf("got %d terminal events, want one", len(ends))
			}
			got := ends[0]
			if name == "code_mode" || name == "shell" {
				if got.ToolOutput != "" || len(got.ToolContentParts) != 0 {
					t.Fatal("unrelated/legacy tool output retained in UI events")
				}
				return
			}
			if got.ToolOutput != receipt || len(got.ToolContentParts) != 2 || got.ToolContentParts[0].Text != "visible output" || got.ToolContentParts[1].Text != "\n"+receipt {
				t.Fatalf("script receipt lost or reordered: %+v", got)
			}
		})
	}
}

func TestStreamAdapterScriptReceiptsDoNotRetainImageBytes(t *testing.T) {
	image := &core.ToolImageData{MediaType: "image/png", Base64: "large-image-payload"}
	parts := []core.ToolContentPart{
		{Type: core.ToolContentPartText, Text: "before"},
		{Type: core.ToolContentPartImageData, ImageData: image},
		{Type: core.ToolContentPartText, Text: "after"},
	}
	adapter := NewStreamAdapter(10)
	go adapter.ProcessStream(context.Background(), &testStream{events: []core.Event{{
		Type: core.EventToolExecEnd, ToolCallID: "call-1", ToolName: "spawn_script",
		ToolSuccess: true, ToolContentParts: parts, ToolImages: []string{"image.png"},
	}}})
	var got []core.ToolContentPart
	var mediaPath string
	for event := range adapter.Events() {
		if event.Type == StreamEventToolEnd {
			got = event.ToolContentParts
		}
		if event.Type == StreamEventImage {
			mediaPath = event.ImagePath
		}
	}
	if len(got) != 3 || got[0].Text != "before" || got[1].Type != core.ToolContentPartImageData || got[1].ImageData != nil || got[2].Text != "after" {
		t.Fatalf("image payload retained or output order lost: %+v", got)
	}
	if parts[1].ImageData != image || parts[1].ImageData.Base64 != "large-image-payload" || mediaPath != "image.png" {
		t.Fatal("stripping UI receipt image bytes changed engine parts or media events")
	}
}
