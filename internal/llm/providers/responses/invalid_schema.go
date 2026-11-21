package responses

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

// invalidFunctionParametersCode is the Responses API error code for a function
// tool whose parameters schema the backend refuses, typically because a strict
// schema uses a keyword strict mode does not support. The message names the
// function.
const invalidFunctionParametersCode = "invalid_function_parameters"

// invalidJSONSchemaCode is reported (with param "tools" and no function name)
// when the backend cannot compile a strict schema, e.g. a regex lookaround.
const invalidJSONSchemaCode = "invalid_json_schema"

var invalidFunctionSchemaNameRE = regexp.MustCompile(`Invalid schema for function '([^']+)'`)

// InvalidFunctionSchemaTool reports whether err is the backend rejecting a
// function tool's parameters schema and, when the message names it, the tool.
// The rejection is deterministic: resending the same tools fails the same way.
func InvalidFunctionSchemaTool(err error) (string, bool) {
	apiErr := invalidFunctionParametersError(err)
	if apiErr == nil {
		return "", false
	}
	if m := invalidFunctionSchemaNameRE.FindStringSubmatch(apiErr.Message); m != nil {
		return m[1], true
	}
	return "", true
}

func invalidFunctionParametersError(err error) *responsesError {
	if err == nil {
		return nil
	}
	var eventErr *responsesAPIEventError
	if errors.As(err, &eventErr) && eventErr.APIError != nil {
		if isInvalidToolSchemaError(eventErr.APIError) {
			return eventErr.APIError
		}
		return nil
	}
	var statusErr *providerhttp.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
		return nil
	}
	var payload struct {
		Error *responsesError `json:"error"`
	}
	if json.Unmarshal([]byte(statusErr.Body), &payload) != nil || payload.Error == nil {
		return nil
	}
	if !isInvalidToolSchemaError(payload.Error) {
		return nil
	}
	return payload.Error
}

func isInvalidToolSchemaError(apiErr *responsesError) bool {
	switch apiErr.Code {
	case invalidFunctionParametersCode:
		return true
	case invalidJSONSchemaCode:
		return apiErr.Param == "tools" || strings.HasPrefix(apiErr.Param, "tools[") || strings.HasPrefix(apiErr.Param, "tools.")
	default:
		return false
	}
}
