package core

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// projectControlOutput is a last-resort projection (including historical
// compaction). Fresh observations are budgeted before draining the live queue.
// Preserve ordered media and as much text as fits, never replace everything
// with status or reinterpret the success of an explicit cancel operation.
func projectControlOutput(output ToolOutput, envelope map[string]json.RawMessage, maxChars int) ToolOutput {
	envelope["output_truncated"] = json.RawMessage(`true`)
	envelope["omitted_output"] = json.RawMessage(`true`)
	raw, err := MarshalCodeModeJSON(envelope)
	if err != nil || len(raw) >= maxChars {
		minimal := map[string]json.RawMessage{}
		for _, key := range []string{"cell_id", "tool_call_id", "status", "state_committed", "error_code", "has_more_output", "output_limited"} {
			if value, ok := envelope[key]; ok && !((key == "has_more_output" || key == "output_limited") && string(value) == "false") {
				minimal[key] = value
			}
		}
		if calls, ok := envelope["calls"]; ok && len(calls) > 2 {
			minimal["audit_omitted"] = json.RawMessage(`true`)
		}
		var unsettled []json.RawMessage
		if json.Unmarshal(envelope["unsettled_calls"], &unsettled) == nil && len(unsettled) > 0 {
			minimal["unsettled_call_count"], _ = json.Marshal(len(unsettled))
			minimal["outcome_unknown"] = json.RawMessage(`true`)
		}
		minimal["output_truncated"] = json.RawMessage(`true`)
		minimal["omitted_output"] = json.RawMessage(`true`)
		raw, err = MarshalCodeModeJSON(minimal)
		if err != nil || len(raw) > maxChars {
			return cellProjectionFailure(output)
		}
	}
	parts, media := projectControlParts(output, max(0, maxChars-len(raw)-1))
	output.Media = media
	output.Content = string(raw)
	output.ContentParts = append(parts, ToolContentPart{Type: ToolContentPartText, Text: "\n" + output.Content})
	return output
}

func projectControlParts(output ToolOutput, remaining int) ([]ToolContentPart, []MediaArtifact) {
	parts := make([]ToolContentPart, 0, len(output.ContentParts)+1)
	media := newCodeModeProjectedMedia(output.Media)
	for i, part := range output.ContentParts {
		if i == len(output.ContentParts)-1 && part.Type == ToolContentPartText && strings.TrimSpace(part.Text) == output.Content {
			continue // only the trailing status envelope, not deliberately emitted JSON
		}
		if part.Type == ToolContentPartText {
			remaining = media.keepText(part.Text, remaining)
			if len(part.Text) > remaining {
				part.Text = part.Text[:remaining]
				for !utf8.ValidString(part.Text) {
					part.Text = part.Text[:len(part.Text)-1]
				}
			}
			remaining -= len(part.Text)
			if part.Text == "" {
				continue
			}
		}
		parts = append(parts, part)
	}
	return parts, media.filter(output.Media)
}

// Track only URLs that were complete before projection, so cutting a longer
// URL cannot turn it into a matching reference. Never scan the receipt.
type codeModeProjectedMedia map[string]bool

func newCodeModeProjectedMedia(media []MediaArtifact) codeModeProjectedMedia {
	if len(media) == 0 {
		return nil
	}
	retained := make(codeModeProjectedMedia, len(media))
	for _, artifact := range media {
		retained[artifact.Reference] = false
	}
	return retained
}

func (m codeModeProjectedMedia) keepText(text string, limit int) int {
	if m != nil {
		for start, url := range CodeModeMediaURLs(text) {
			if start >= limit {
				break
			}
			ref := strings.TrimPrefix(url, CodeModeMediaURLPrefix)
			if _, known := m[ref]; !known {
				continue
			}
			if start+len(url) > limit {
				return start
			}
			m[ref] = true
		}
	}
	return limit
}

func (m codeModeProjectedMedia) filter(media []MediaArtifact) []MediaArtifact {
	var retained []MediaArtifact
	for _, artifact := range media {
		if m[artifact.Reference] {
			retained = append(retained, artifact)
		}
	}
	return retained
}
