package responsesproviders

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/credentials"
)

func resetChatGPTStrictRejections(t *testing.T) {
	t.Helper()
	reset := func() {
		chatGPTStrictRejections.mu.Lock()
		chatGPTStrictRejections.keys = nil
		chatGPTStrictRejections.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

type strictFallbackServer struct {
	mu       sync.Mutex
	requests []map[string]bool // tool name -> strict, per request
	reject   func(strict map[string]bool) *http.Response
}

func (s *strictFallbackServer) roundTrip(t *testing.T) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var payload struct {
			Tools []struct {
				Name   string `json:"name"`
				Strict bool   `json:"strict"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		strict := map[string]bool{}
		for _, tool := range payload.Tools {
			strict[tool.Name] = tool.Strict
		}
		s.mu.Lock()
		s.requests = append(s.requests, strict)
		s.mu.Unlock()
		if resp := s.reject(strict); resp != nil {
			return resp, nil
		}
		return sseResponse(
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"bad_tool"}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"bad_tool","arguments":"{\"ids\":[1],\"note\":null}"}}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		), nil
	}
}

func sseResponse(lines ...string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(append(lines, "data: [DONE]"), "\n\n") + "\n\n")),
	}
}

// Draft-4 boolean exclusiveMinimum passes the local strict check but the
// backend rejects it (probed live), naming the function.
const strictSchemaRejection = `Invalid schema for function 'bad_tool': true is not of type \"number\"`

func strictFallbackTools() []ToolSpec {
	return []ToolSpec{
		{
			Name: "bad_tool",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ids":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number", "minimum": 0, "exclusiveMinimum": true}},
					"note": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"ids"},
			},
		},
		{
			Name: "good_tool",
			Schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"a": map[string]interface{}{"type": "string"}},
				"required":   []interface{}{"a"},
			},
		},
	}
}

func runStrictFallbackStream(t *testing.T) (*ToolCall, error) {
	t.Helper()
	provider := NewChatGPTProviderWithCreds(&credentials.ChatGPTCredentials{
		AccessToken: "test-token",
		AccountID:   "test-account",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}, "gpt-6.1-sol-medium")
	stream, err := provider.Stream(context.Background(), Request{
		Messages: []Message{UserText("hello")},
		Tools:    strictFallbackTools(),
	})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var call *ToolCall
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return call, nil
		}
		if err != nil {
			return call, err
		}
		switch event.Type {
		case EventError:
			return call, event.Err
		case EventToolCall:
			call = event.Tool
		case EventDone:
			return call, nil
		}
	}
}

func TestChatGPTStreamResendsNonStrictWhenStrictSchemaRejected(t *testing.T) {
	tests := []struct {
		name   string
		reject func() *http.Response
	}{
		{
			// What the ChatGPT backend actually does: HTTP 200 with an error event.
			name: "in-stream error event",
			reject: func() *http.Response {
				return sseResponse(`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_function_parameters","message":"` + strictSchemaRejection + `"}}`)
			},
		},
		{
			name: "synchronous 400",
			reject: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Status:     "400 Bad Request",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"invalid_function_parameters","message":"` + strictSchemaRejection + `"}}`)),
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetChatGPTStrictRejections(t)
			origClient := chatGPTHTTPClient
			defer func() { chatGPTHTTPClient = origClient }()
			server := &strictFallbackServer{reject: func(strict map[string]bool) *http.Response {
				if strict["bad_tool"] {
					return tt.reject()
				}
				return nil
			}}
			chatGPTHTTPClient = &http.Client{Transport: server.roundTrip(t)}

			call, err := runStrictFallbackStream(t)
			if err != nil {
				t.Fatalf("stream should recover from the strict rejection, got %v", err)
			}
			if call == nil || call.Name != "bad_tool" {
				t.Fatalf("expected bad_tool call after resend, got %#v", call)
			}
			var args map[string]any
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				t.Fatalf("decode args: %v", err)
			}
			if v, ok := args["note"]; !ok || v != nil {
				t.Fatalf("non-strict resend must pass the model's nulls through unchanged, got %s", call.Arguments)
			}
			if len(server.requests) != 2 {
				t.Fatalf("requests = %d, want rejected strict request plus one resend", len(server.requests))
			}
			if first := server.requests[0]; !first["bad_tool"] || !first["good_tool"] {
				t.Fatalf("first request should send both tools strict: %v", first)
			}
			if resend := server.requests[1]; resend["bad_tool"] || resend["good_tool"] {
				t.Fatalf("resend should send every upgraded tool non-strict: %v", resend)
			}

			// The rejection is remembered: the next request keeps the good tool
			// strict and does not try the rejected schema again.
			if _, err := runStrictFallbackStream(t); err != nil {
				t.Fatalf("second stream: %v", err)
			}
			if len(server.requests) != 3 {
				t.Fatalf("requests = %d, want one more request without a rejection", len(server.requests))
			}
			if next := server.requests[2]; next["bad_tool"] || !next["good_tool"] {
				t.Fatalf("next request should keep only good_tool strict: %v", next)
			}
		})
	}
}

func TestChatGPTStreamSurfacesUnrelatedErrorsWithoutResend(t *testing.T) {
	resetChatGPTStrictRejections(t)
	origClient := chatGPTHTTPClient
	defer func() { chatGPTHTTPClient = origClient }()
	server := &strictFallbackServer{reject: func(map[string]bool) *http.Response {
		return sseResponse(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too long"}}`)
	}}
	chatGPTHTTPClient = &http.Client{Transport: server.roundTrip(t)}

	_, err := runStrictFallbackStream(t)
	if err == nil || !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Fatalf("expected the original error, got %v", err)
	}
	if len(server.requests) != 1 {
		t.Fatalf("requests = %d, want no resend for unrelated errors", len(server.requests))
	}
}

func TestChatGPTStrictRejectionNotRememberedWhenResendAlsoFails(t *testing.T) {
	resetChatGPTStrictRejections(t)
	origClient := chatGPTHTTPClient
	defer func() { chatGPTHTTPClient = origClient }()
	// The backend refuses the schema whether or not it is strict, so the
	// strict upgrade is not to blame.
	server := &strictFallbackServer{reject: func(map[string]bool) *http.Response {
		return sseResponse(`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_function_parameters","message":"` + strictSchemaRejection + `"}}`)
	}}
	chatGPTHTTPClient = &http.Client{Transport: server.roundTrip(t)}

	if _, err := runStrictFallbackStream(t); err == nil || !strings.Contains(err.Error(), "invalid_function_parameters") {
		t.Fatalf("expected the rejection to surface after the failed resend, got %v", err)
	}
	if len(server.requests) != 2 {
		t.Fatalf("requests = %d, want strict request plus one resend", len(server.requests))
	}
	_, _ = runStrictFallbackStream(t)
	if next := server.requests[2]; !next["bad_tool"] {
		t.Fatalf("a rejection that non-strict also hit must not disable strict: %v", next)
	}
}

func TestChatGPTStreamUnnamedStrictRejectionDisablesUpgradedTools(t *testing.T) {
	resetChatGPTStrictRejections(t)
	origClient := chatGPTHTTPClient
	defer func() { chatGPTHTTPClient = origClient }()
	server := &strictFallbackServer{reject: func(strict map[string]bool) *http.Response {
		if !strict["bad_tool"] {
			return nil
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_json_schema","message":"Invalid JSON schema: regex lookaround is not supported. Found at $.properties.x.pattern.","param":"tools","type":"invalid_request_error"}}`)),
		}
	}}
	chatGPTHTTPClient = &http.Client{Transport: server.roundTrip(t)}

	if _, err := runStrictFallbackStream(t); err != nil {
		t.Fatalf("stream should recover from the unnamed rejection, got %v", err)
	}
	if _, err := runStrictFallbackStream(t); err != nil {
		t.Fatalf("second stream: %v", err)
	}
	if len(server.requests) != 3 {
		t.Fatalf("requests = %d, want rejected request, resend, then a clean request", len(server.requests))
	}
	if next := server.requests[2]; next["bad_tool"] || next["good_tool"] {
		t.Fatalf("an unnamed rejection should leave every upgraded tool non-strict: %v", next)
	}
}
