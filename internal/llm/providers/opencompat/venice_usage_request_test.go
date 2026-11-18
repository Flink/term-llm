package opencompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVeniceStreamUsageRequest(t *testing.T) {
	for _, tc := range []struct {
		name            string
		noStreamOptions bool
	}{
		{name: "requests terminal usage by default"},
		{name: "omits stream options when disabled", noStreamOptions: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "venice-uncensored"
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if string(body["stream"]) != "true" || string(body["model"]) != `"`+model+`"` {
					t.Errorf("stream = %s, model = %s", body["stream"], body["model"])
				}
				options, present := body["stream_options"]
				if tc.noStreamOptions {
					if present {
						t.Errorf("stream_options = %s, want omitted", options)
					}
				} else {
					var got struct {
						IncludeUsage bool `json:"include_usage"`
					}
					if err := json.Unmarshal(options, &got); err != nil || !got.IncludeUsage {
						t.Errorf("stream_options = %s, want include_usage=true (decode error: %v)", options, err)
						http.Error(w, "stream_options.include_usage required", http.StatusBadRequest)
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n", model)
				_, _ = fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", model)
				if !tc.noStreamOptions {
					_, _ = fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":37,\"total_tokens\":157,\"prompt_tokens_details\":{\"cached_tokens\":40,\"cache_creation_input_tokens\":10},\"completion_tokens_details\":{\"reasoning_tokens\":12}}}\n\n", model)
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer ts.Close()

			provider := &VeniceProvider{OpenAICompatProvider: NewOpenAICompatProvider(ts.URL, "test-key", model, "Venice")}
			if tc.noStreamOptions {
				provider.SetNoStreamOptions(true)
			}
			stream, err := provider.Stream(context.Background(), Request{Messages: []Message{UserText("hello")}})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()

			var gotUsage *Usage
			var text string
			var done bool
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Recv: %v", err)
				}
				switch event.Type {
				case EventTextDelta:
					text += event.Text
				case EventUsage:
					if gotUsage != nil || event.Use == nil || done {
						t.Fatalf("unexpected usage event: %+v", event)
					}
					gotUsage = event.Use
				case EventDone:
					done = true
				case EventError:
					t.Fatalf("stream error: %v", event.Err)
				}
			}
			if text != "hello" || !done {
				t.Fatalf("text = %q, done = %v; want hello and done", text, done)
			}
			if tc.noStreamOptions {
				if gotUsage != nil {
					t.Fatalf("usage = %+v, want none without upstream usage", gotUsage)
				}
				return
			}
			want := Usage{
				InputTokens: 70, OutputTokens: 37, CachedInputTokens: 40, CacheWriteTokens: 10,
				ProviderRawInputTokens: 120, ProviderTotalTokens: 157, ReasoningTokens: 12,
			}
			if gotUsage == nil || *gotUsage != want {
				t.Fatalf("usage = %+v, want %+v", gotUsage, want)
			}
		})
	}
}
