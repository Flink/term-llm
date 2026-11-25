package mcpweb

import (
	"errors"
	"net/url"
)

const OAuthCallbackPath = "/v1/mcp/oauth/callback"

// ParseOAuthCallbackQuery validates a raw OAuth callback query and returns only
// the code, state, iss, and error parameters that were present.
func ParseOAuthCallbackQuery(raw string) (url.Values, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("malformed OAuth callback query")
	}
	callback := make(url.Values, 4)
	for _, key := range []string{"code", "state", "iss", "error"} {
		if values, ok := query[key]; ok {
			if len(values) != 1 {
				return nil, errors.New("duplicate OAuth callback parameter")
			}
			callback[key] = values
		}
	}
	if callback.Get("state") == "" {
		return nil, errors.New("OAuth callback requires state")
	}
	code, hasCode := callback["code"]
	oauthError, hasError := callback["error"]
	if hasCode == hasError || (hasCode && code[0] == "") || (hasError && oauthError[0] == "") {
		return nil, errors.New("OAuth callback requires exactly one nonempty code or error")
	}
	return callback, nil
}
