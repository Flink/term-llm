package opencompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatTerminationReason(t *testing.T) {
	providers := []struct {
		name        string
		newProvider func(string) Provider
	}{
		{"compat", func(url string) Provider { return NewOpenAICompatProvider(url, "key", "model", "test") }},
		{"xai", func(url string) Provider { return &XAIProvider{baseURL: url, apiKey: "key", model: "model"} }},
		{"venice", func(url string) Provider {
			return &VeniceProvider{OpenAICompatProvider: NewOpenAICompatProvider(url, "key", "model", "Venice")}
		}},
	}
	cases := []struct {
		name      string
		reason    string
		tool      bool
		text      string
		wantError bool
		apiError  bool
	}{
		{name: "stop", reason: "stop", text: "hello"},
		{name: "tool", reason: "tool_calls", tool: true},
		{name: "text limit", reason: "length", text: "hello"},
		{name: "tool limit", reason: "length", tool: true, wantError: true},
		{name: "empty limit", reason: "length", wantError: true},
		{name: "filtered", reason: "content_filter", text: "hello"},
		{name: "raw unknown", reason: " vendor_reason ", text: "hello"},
		{name: "missing", text: "hello"},
		{name: "error after reason", reason: " vendor_reason ", text: "hello", apiError: true, wantError: true},
	}
	for _, provider := range providers {
		for _, tc := range cases {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tc.text)
					if tc.tool {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n\n")
					}
					fmt.Fprintf(w, "data: {\"choices\":[{\"finish_reason\":%q}]}\n\n", tc.reason)
					if tc.apiError {
						io.WriteString(w, "data: {\"error\":{\"message\":\"test failure\"}}\n\n")
					}
					io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				s, err := provider.newProvider(server.URL).Stream(context.Background(), Request{Messages: []Message{UserText("hi")}})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				var done, errorEvents, tools int
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
					case EventDone:
						done++
						if ev.TerminationReason != tc.reason {
							t.Errorf("reason = %q, want %q", ev.TerminationReason, tc.reason)
						}
					case EventError:
						errorEvents++
						if !tc.wantError {
							t.Fatalf("unexpected error: %v", ev.Err)
						}
						if tc.apiError {
							if !strings.Contains(ev.Err.Error(), "API error: test failure") {
								t.Fatalf("unexpected API error: %v", ev.Err)
							}
						} else {
							var limit *OutputLimitError
							if !errors.As(ev.Err, &limit) || limit.DebugFields()["termination_reason"] != tc.reason {
								t.Fatalf("expected output limit with raw reason: %v", ev.Err)
							}
						}
						var fields interface{ DebugFields() map[string]any }
						if !errors.As(ev.Err, &fields) || fields.DebugFields()["termination_reason"] != tc.reason {
							t.Fatalf("missing raw reason on error: %v", ev.Err)
						}
					case EventToolCall:
						tools++
					case EventTextDelta:
						text += ev.Text
					}
				}
				wantDone, wantErrors, wantTools := 1, 0, 0
				if tc.wantError {
					wantDone, wantErrors = 0, 1
				} else if tc.tool {
					wantTools = 1
				}
				if done != wantDone || errorEvents != wantErrors || tools != wantTools || text != tc.text {
					t.Fatalf("done/errors/tools/text = %d/%d/%d/%q, want %d/%d/%d/%q", done, errorEvents, tools, text, wantDone, wantErrors, wantTools, tc.text)
				}
			})
		}
	}
}
