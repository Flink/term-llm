package responses

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

var codexModelRejectionRE = regexp.MustCompile(`^The '[^'\s]+' model is not supported when using Codex with a ChatGPT account\.$`)

// IsModelRejection reports confirmed model-selection rejections in wrapped HTTP
// or Responses SSE/WebSocket API errors, not general request or transport errors.
func IsModelRejection(err error) bool {
	var apiErr *responsesError
	var eventErr *responsesAPIEventError
	if errors.As(err, &eventErr) {
		if eventErr == nil || (eventErr.Status != 0 && eventErr.Status != http.StatusBadRequest && eventErr.Status != http.StatusNotFound) {
			return false
		}
		apiErr = eventErr.APIError
	} else {
		var statusErr *providerhttp.StatusError
		if !errors.As(err, &statusErr) || statusErr == nil || (statusErr.StatusCode != http.StatusBadRequest && statusErr.StatusCode != http.StatusNotFound) {
			return false
		}
		var payload struct {
			responsesError
			Error  *responsesError `json:"error"`
			Detail string          `json:"detail"`
		}
		if json.Unmarshal([]byte(statusErr.Body), &payload) != nil {
			return codexModelRejectionRE.MatchString(strings.TrimSpace(statusErr.Body))
		}
		apiErr = payload.Error
		if apiErr == nil {
			apiErr = &payload.responsesError
			if apiErr.Message == "" {
				apiErr.Message = payload.Detail
			}
		}
	}
	if apiErr == nil || (apiErr.Param != "" && apiErr.Param != "model") {
		return false
	}
	if apiErr.Type != "" && apiErr.Type != "invalid_request_error" {
		return false
	}
	switch apiErr.Code {
	case "model_not_found", "unsupported_model":
		return true
	case "":
		return codexModelRejectionRE.MatchString(strings.TrimSpace(apiErr.Message))
	default:
		return false
	}
}
