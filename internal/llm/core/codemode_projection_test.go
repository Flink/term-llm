package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodeModeFormatGuardSurvivesPersistenceAndCompaction(t *testing.T) {
	output := ToolOutput{PreserveJSON: true, Content: `{"status":"completed","state_committed":true,"state":{"n":7},"output":[{"kind":"text","value":"` + strings.Repeat("x", 200) + `"}]}`}
	msg := ToolResultMessageFromOutput("cell", "code_mode", output, nil)
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	result := restored.Parts[0].ToolResult
	if !result.PreserveJSON {
		t.Fatal("lost format guard")
	}
	for _, budget := range []int{1, 100, 1000} {
		out := TruncateToolResultValue(*result, budget)
		if !json.Valid([]byte(out.Content)) {
			t.Fatalf("%d: invalid JSON %q", budget, out.Content)
		}
	}
}
func TestCodeModeTextPartsCountAgainstProjectionBudget(t *testing.T) {
	output := ToolOutput{PreserveJSON: true, Content: `{"status":"completed","state_committed":true,"state":{}}`, ContentParts: []ToolContentPart{{Type: ToolContentPartText, Text: strings.Repeat("x", 1000)}}}
	got := TruncateToolOutput(output, 100)
	if !strings.Contains(got.Content, "omitted_output") || !json.Valid([]byte(got.ContentParts[len(got.ContentParts)-1].Text)) {
		t.Fatalf("%+v", got)
	}
	if len(ToolResultTextContent(&ToolResult{ContentParts: got.ContentParts})) > 100 {
		t.Fatalf("projection exceeded its text budget: %+v", got)
	}
}

func TestControlEnvelopeTruncationIsTruthfulNotFailure(t *testing.T) {
	output := ToolOutput{
		PreserveJSON: true,
		Content:      `{"cell_id":"cm_1","status":"completed","state_committed":true,"has_more_output":false}`,
		ContentParts: []ToolContentPart{{Type: ToolContentPartText, Text: strings.Repeat("x", 1000)}},
	}
	got := TruncateToolOutput(output, 120)
	if got.IsError {
		t.Fatalf("successful cell became an error: %+v", got)
	}
	if len(got.ContentParts) == 0 || !json.Valid([]byte(got.ContentParts[len(got.ContentParts)-1].Text)) {
		t.Fatalf("invalid control projection: %+v", got.ContentParts)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got.Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope["cell_id"]) != `"cm_1"` || string(envelope["status"]) != `"completed"` || string(envelope["state_committed"]) != "true" {
		t.Fatalf("lost truthful metadata: %s", got.Content)
	}
	if string(envelope["output_truncated"]) != "true" {
		t.Fatalf("truncation not reported: %s", got.Content)
	}
}

func TestControlProjectionKeepsImagesIdentityAndCancelSuccess(t *testing.T) {
	raw := `{"cell_id":"cm_long","tool_call_id":"current-observer","status":"cancelled","state_committed":false,"has_more_output":true,"calls":[{"large":"` + strings.Repeat("x", 2000) + `"}]}`
	output := ToolOutput{PreserveJSON: true, Content: raw, ContentParts: []ToolContentPart{
		{Type: ToolContentPartText, Text: strings.Repeat("a", 1000)},
		{Type: ToolContentPartImageData, ImageData: &ToolImageData{MediaType: "image/png", Base64: "abc"}},
		{Type: ToolContentPartText, Text: "\n" + raw},
	}}
	got := ProjectCodeModeOutput(output, 512)
	if got.IsError || !strings.Contains(got.Content, `"tool_call_id":"current-observer"`) || !strings.Contains(got.Content, `"has_more_output":true`) {
		t.Fatalf("control semantics/identity lost: %+v", got)
	}
	images := 0
	for _, part := range got.ContentParts {
		if part.Type == ToolContentPartImageData {
			images++
		}
	}
	if images != 1 || len(ToolResultTextContent(&ToolResult{ContentParts: got.ContentParts})) > 512 {
		t.Fatalf("image was lost or text budget exceeded: %+v", got)
	}
}

func TestCodeModeProjectionFiltersMediaOutsideRetainedEmissions(t *testing.T) {
	kept := MediaArtifact{Reference: strings.Repeat("a", 32), StoredPath: "/private/kept.png"}
	clipped := MediaArtifact{Reference: strings.Repeat("b", 32), StoredPath: "/private/clipped.png"}
	keptURL := CodeModeMediaURLPrefix + kept.Reference
	clippedURL := CodeModeMediaURLPrefix + clipped.Reference
	for _, legacy := range []bool{false, true} {
		// The clipped URL also occurs in the receipt; that copy must not grant it.
		raw := `{"status":"completed","error":"` + clippedURL + `"}`
		if legacy {
			raw = `{"status":"completed","error":"` + clippedURL + `","output":[{"kind":"text","value":"` + keptURL + `"}]}`
		}
		output := ToolOutput{Content: raw, PreserveJSON: true, Media: []MediaArtifact{kept, clipped}, ContentParts: []ToolContentPart{
			{Type: ToolContentPartText, Text: keptURL + "\n" + clippedURL + strings.Repeat(" padded", 50)},
			{Type: ToolContentPartText, Text: "\n" + raw},
		}}
		projected := ProjectCodeModeOutput(output, len(raw)+len(keptURL)+65)
		if legacy {
			if len(projected.Media) > 0 || !strings.Contains(projected.Content, "omitted_output") {
				t.Fatalf("legacy receipt granted omitted output: %+v", projected)
			}
		} else {
			if len(projected.Media) != 1 || projected.Media[0] != kept || !strings.Contains(projected.Content, clippedURL) {
				t.Fatalf("want only the URL retained in explicit output, not the receipt: %+v", projected)
			}
			if got := projected.ContentParts[0].Text; got != keptURL+"\n" {
				t.Fatalf("projection left a partial URL: %q", got)
			}
		}
		if got := ProjectCodeModeOutput(output, 1); len(got.Media) != 0 || !got.IsError {
			t.Fatalf("projection failure retained a media grant: %+v", got)
		}
	}
}
