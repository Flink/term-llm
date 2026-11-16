package debuglog

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormatTerminationReason(t *testing.T) {
	for _, eventType := range []string{"done", "error"} {
		t.Run(eventType, func(t *testing.T) {
			session := &Session{Entries: []any{EventEntry{EventType: eventType, Data: map[string]any{"termination_reason": "max_output_tokens", "error": "incomplete"}}}}
			var out bytes.Buffer
			FormatSession(&out, session, FormatOptions{NoColor: true})
			if !strings.Contains(out.String(), `termination_reason="max_output_tokens"`) {
				t.Fatalf("missing reason: %s", out.String())
			}
		})
	}
}

func TestRedactTerminationReason(t *testing.T) {
	got := redactObject(map[string]any{"termination_reason": "end_turn"}, "data")
	if got["termination_reason"] != "end_turn" {
		t.Fatalf("reason not preserved: %v", got)
	}
	got = redactObject(map[string]any{"termination_reason": "Bearer secret-value"}, "data")
	if got["termination_reason"] != redacted {
		t.Fatalf("sensitive reason not redacted: %v", got)
	}
}

func TestTailTerminationReason(t *testing.T) {
	var out bytes.Buffer
	FormatTailEntry(&out, []byte(`{"timestamp":"2026-11-16T05:07:11Z","type":"event","event_type":"done","data":{"termination_reason":"stop"}}`))
	if !strings.Contains(out.String(), `termination_reason="stop"`) {
		t.Fatalf("missing reason: %s", out.String())
	}
}
