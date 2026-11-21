package responses

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

func TestIsModelRejection(t *testing.T) {
	const codex = "The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."
	httpErr := func(status int, body string) error {
		return &providerhttp.StatusError{StatusCode: status, Body: body}
	}
	h := &responsesStreamEventHandler{}
	eventErr := h.handleResponseError(responsesEventContext{
		data: []byte(`{"response":{"error":{"code":"model_not_found"}}}`),
	})
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"wrapped model not found", fmt.Errorf("stream: %w", httpErr(404, `{"error":{"code":"model_not_found","type":"invalid_request_error"}}`)), true},
		{"unsupported model", httpErr(400, `{"error":{"code":"unsupported_model","param":"model"}}`), true},
		{"Codex detail", httpErr(400, `{"detail":"`+codex+`"}`), true},
		{"Codex message", httpErr(400, `{"message":"`+codex+`"}`), true},
		{"Codex plain", httpErr(400, codex+"\n"), true},
		{"decoded API event", fmt.Errorf("stream: %w", eventErr), true},
		{"schema", httpErr(400, `{"error":{"code":"invalid_function_parameters","message":"Invalid schema for function 'model_not_found'"}}`), false},
		{"parameter", httpErr(400, `{"error":{"code":"unsupported_parameter","param":"temperature","message":"`+codex+`"}}`), false},
		{"credentials", httpErr(http.StatusUnauthorized, `{"error":{"code":"model_not_found"}}`), false},
		{"transport", context.DeadlineExceeded, false},
		{"server failure", httpErr(500, `{"error":{"code":"model_not_found"}}`), false},
		{"unrelated 404", httpErr(404, "Not Found"), false},
		{"generic unsupported", httpErr(400, `{"message":"unsupported model parameter"}`), false},
		{"untyped diagnostic", fmt.Errorf("stream: %s", codex), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelRejection(tt.err); got != tt.want {
				t.Errorf("IsModelRejection(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
