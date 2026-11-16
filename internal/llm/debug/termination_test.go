package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/core/stream"
)

func TestLoggerTerminationReason(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event Event
		want  string
	}{
		{"done", Event{Type: EventDone, TerminationReason: "stop"}, "stop"},
		{"error", Event{Type: EventError, Err: fmt.Errorf("provider: %w", &stream.ResponsesIncompleteError{Reason: "max_output_tokens"})}, "max_output_tokens"},
		{"unknown", Event{Type: EventDone}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logger, err := NewLogger(dir, "termination")
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			logger.LogEvent(tc.event)
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "termination.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var entry struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				t.Fatal(err)
			}
			got, _ := entry.Data["termination_reason"].(string)
			if got != tc.want {
				t.Fatalf("termination_reason = %q, want %q", got, tc.want)
			}
		})
	}
}
