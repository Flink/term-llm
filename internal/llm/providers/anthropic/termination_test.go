package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/core/stream"
)

func TestAnthropicTerminationReason(t *testing.T) {
	for _, search := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			reason    string
			truncated bool
		}{
			{name: "end turn", reason: "end_turn"},
			{name: "length", reason: "max_tokens"},
			{name: "tool", reason: "tool_use"},
			{name: "pause", reason: "pause_turn"},
			{name: "raw unknown", reason: " vendor_reason "},
			{name: "missing"},
			{name: "truncated", reason: "max_tokens", truncated: true},
		} {
			t.Run(fmt.Sprintf("search=%t/%s", search, tc.name), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
					io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
					fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q}}\n\n", tc.reason)
					io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n")
					if !tc.truncated {
						io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}
				}))
				defer server.Close()
				s, err := newAnthropicTestProvider(t, server.URL, "claude-sonnet-4-6").Stream(context.Background(), core.Request{Messages: []core.Message{core.UserText("hi")}, Search: search})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				var done, errorEvents int
				var text string
				for {
					ev, err := s.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					switch ev.Type {
					case core.EventDone:
						done++
						if ev.TerminationReason != tc.reason {
							t.Errorf("reason = %q, want %q", ev.TerminationReason, tc.reason)
						}
					case core.EventError:
						errorEvents++
						var incomplete *stream.StreamIncompleteError
						if !tc.truncated || !errors.As(ev.Err, &incomplete) {
							t.Fatalf("unexpected error: %v", ev.Err)
						}
						var fields interface{ DebugFields() map[string]any }
						if !errors.As(ev.Err, &fields) || fields.DebugFields()["termination_reason"] != tc.reason {
							t.Fatalf("missing raw reason on error: %v", ev.Err)
						}
					case core.EventTextDelta:
						text += ev.Text
					}
				}
				wantDone, wantErrors := 1, 0
				if tc.truncated {
					wantDone, wantErrors = 0, 1
				}
				if done != wantDone || errorEvents != wantErrors || text != "hello" {
					t.Fatalf("done/errors/text = %d/%d/%q", done, errorEvents, text)
				}
			})
		}
	}
}
