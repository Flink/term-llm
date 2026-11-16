package ollama

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestOllamaTerminationReason(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reason    string
		wantError bool
	}{
		{name: "stop", reason: "stop"},
		{name: "length", reason: "length"},
		{name: "raw unknown", reason: " vendor_reason "},
		{name: "missing"},
		{name: "error", reason: "length", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				io.WriteString(w, "{\"message\":{\"content\":\"hello\"}}\n")
				errField := ""
				if tc.wantError {
					errField = `,"error":"test failure"`
				}
				fmt.Fprintf(w, "{\"done\":true,\"done_reason\":%q%s}\n", tc.reason, errField)
				io.WriteString(w, "{\"done\":true,\"prompt_eval_count\":1,\"eval_count\":1}\n")
			}))
			defer server.Close()
			s, err := New(server.URL, "model", Options{}).Stream(context.Background(), core.Request{Messages: []core.Message{core.UserText("hi")}})
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
					if !tc.wantError || !strings.Contains(ev.Err.Error(), "Ollama API error: test failure") {
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
			if tc.wantError {
				wantDone, wantErrors = 0, 1
			}
			if done != wantDone || errorEvents != wantErrors || text != "hello" {
				t.Fatalf("done/errors/text = %d/%d/%q", done, errorEvents, text)
			}
		})
	}
}
