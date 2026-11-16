package gemini

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestGeminiTerminationReason(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reason    string
		noContent bool
		wantError bool
	}{
		{name: "stop", reason: "STOP"},
		{name: "length", reason: "MAX_TOKENS"},
		{name: "blocked", reason: "SAFETY", wantError: true},
		{name: "recitation", reason: "RECITATION", wantError: true},
		{name: "raw unknown", reason: " vendor_reason ", wantError: true},
		{name: "empty content", reason: "STOP", noContent: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := New("key", "gemini-3-flash-preview")
			provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				content := `,"content":{"parts":[{"text":"hello"}]}`
				if tc.noContent {
					content = ""
				}
				body := fmt.Sprintf("data: {\"candidates\":[{\"finishReason\":%q%s},{\"finishReason\":\"SECONDARY\"}]}\n\n", tc.reason, content)
				body += "data: {\"usageMetadata\":{\"promptTokenCount\":1}}\n\n"
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			s, err := provider.Stream(context.Background(), core.Request{Messages: []core.Message{core.UserText("hi")}})
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
					if !tc.wantError {
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
			wantDone, wantErrors, wantText := 1, 0, "hello"
			if tc.wantError {
				wantDone, wantErrors = 0, 1
			}
			if tc.noContent {
				wantText = ""
			}
			if done != wantDone || errorEvents != wantErrors || text != wantText {
				t.Fatalf("done/errors/text = %d/%d/%q", done, errorEvents, text)
			}
		})
	}
}
