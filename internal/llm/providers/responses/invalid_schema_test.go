package responses

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

const testUniqueItemsRejection = `Invalid schema for function 'probe_tool': At schema path /properties/u, keyword "uniqueItems" is not supported for strict schemas`

func TestInvalidFunctionSchemaTool(t *testing.T) {
	eventErr := &responsesAPIEventError{APIError: &responsesError{Code: "invalid_function_parameters", Message: testUniqueItemsRejection}}
	tests := []struct {
		name     string
		err      error
		wantName string
		wantOK   bool
	}{
		{name: "event error", err: eventErr, wantName: "probe_tool", wantOK: true},
		{name: "wrapped event error", err: fmt.Errorf("stream: %w", eventErr), wantName: "probe_tool", wantOK: true},
		{name: "unnamed", err: &responsesAPIEventError{APIError: &responsesError{Code: "invalid_function_parameters", Message: "bad schema"}}, wantOK: true},
		{name: "other code", err: &responsesAPIEventError{APIError: &responsesError{Code: "context_length_exceeded", Message: testUniqueItemsRejection}}},
		{
			name:     "400 body",
			err:      providerhttp.NewStatusErrorMessageString("x", http.StatusBadRequest, "400", nil, `{"error":{"code":"invalid_function_parameters","message":"Invalid schema for function 'a_b': nope"}}`),
			wantName: "a_b",
			wantOK:   true,
		},
		{
			name:   "uncompilable strict schema",
			err:    providerhttp.NewStatusErrorMessageString("x", http.StatusBadRequest, "400", nil, `{"error":{"code":"invalid_json_schema","message":"Invalid JSON schema: regex lookaround is not supported. Found at $.properties.x.pattern.","param":"tools","type":"invalid_request_error"}}`),
			wantOK: true,
		},
		{name: "invalid_json_schema outside tools", err: &responsesAPIEventError{APIError: &responsesError{Code: "invalid_json_schema", Param: "text.format.schema", Message: "Invalid JSON schema"}}},
		{name: "500 body", err: providerhttp.NewStatusErrorMessageString("x", http.StatusInternalServerError, "500", nil, `{"error":{"code":"invalid_function_parameters","message":"x"}}`)},
		{name: "plain", err: errors.New("invalid_function_parameters")},
		{name: "nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ok := InvalidFunctionSchemaTool(tt.err)
			if name != tt.wantName || ok != tt.wantOK {
				t.Fatalf("InvalidFunctionSchemaTool = (%q, %v), want (%q, %v)", name, ok, tt.wantName, tt.wantOK)
			}
		})
	}
}

type scriptedTestStream struct {
	events []Event
	err    error
}

func (s *scriptedTestStream) Recv() (Event, error) {
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.err != nil {
		return Event{}, s.err
	}
	return Event{}, io.EOF
}

func (s *scriptedTestStream) Close() error { return nil }

// A rejected schema fails identically on every transport, so the WebSocket
// fallback chain must surface it instead of retrying and disabling WebSocket.
func TestWebSocketFallbackDoesNotRetryInvalidFunctionSchema(t *testing.T) {
	rejection := &responsesAPIEventError{APIError: &responsesError{Code: "invalid_function_parameters", Message: testUniqueItemsRejection}}
	tests := []struct {
		name    string
		current Stream
	}{
		{name: "error event", current: &scriptedTestStream{events: []Event{{Type: EventError, Err: rejection}}}},
		{name: "recv error", current: &scriptedTestStream{err: rejection}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disabled, opened := false, false
			stream := &responsesWebSocketFallbackStream{
				current: tt.current,
				fallbacks: []responsesWebSocketFallback{{
					disableWebSocket: func() { disabled = true },
					open: func() (Stream, error) {
						opened = true
						return &scriptedTestStream{}, nil
					},
				}},
			}
			event, err := stream.Recv()
			got := err
			if got == nil {
				got = event.Err
			}
			if _, ok := InvalidFunctionSchemaTool(got); !ok {
				t.Fatalf("expected the schema rejection to surface, got event %#v err %v", event, err)
			}
			if opened || disabled {
				t.Fatalf("fallback opened=%v websocket disabled=%v; want neither", opened, disabled)
			}
		})
	}
}
