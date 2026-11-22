// Package deviceflow adapts provider OAuth device grants to session-owned proxy setup attempts.
package deviceflow

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/term-llm/term-llm/internal/oauth"
)

// Challenge is the entire public start response. Device authorization secrets
// remain in the poll closure and are never serialized.
type Challenge struct {
	ID              string `json:"id"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
}

type Credentials struct {
	AccessToken, RefreshToken, AccountID string
	ExpiresAt                            int64
}
type Poll func(context.Context) (Credentials, error)
type Start func(context.Context, string, http.RoundTripper) (Challenge, Poll, error)

func Label(kind string) string {
	switch kind {
	case "grok":
		return "Grok"
	case "chatgpt":
		return "ChatGPT"
	default:
		return kind
	}
}

// Request bounds the initial exchange by both request and attempt lifetimes.
// Subsequent polling is governed by the attempt, not the HTTP connection.
func (start Start) Request(attempt, request context.Context, kind string, transport http.RoundTripper) (Challenge, Poll, error) {
	ctx, cancel := context.WithTimeout(attempt, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(request, cancel)
	defer stop()
	if start != nil {
		return start(ctx, kind, transport)
	}
	switch kind {
	case "chatgpt":
		return startChatGPT(ctx)
	case "grok":
		return startGrok(ctx, transport)
	default:
		return Challenge{}, nil, errors.New("unknown device issuer")
	}
}

func startChatGPT(ctx context.Context) (Challenge, Poll, error) {
	code, err := oauth.RequestChatGPTDeviceCode(ctx)
	if err != nil {
		return Challenge{}, nil, err
	}
	return Challenge{VerificationURL: oauth.ChatGPTDeviceVerificationURL, UserCode: code.UserCode}, func(ctx context.Context) (Credentials, error) {
		issued, err := oauth.AuthenticateChatGPTDevice(ctx, code)
		if err != nil {
			return Credentials{}, err
		}
		return Credentials{AccessToken: issued.AccessToken, RefreshToken: issued.RefreshToken, AccountID: issued.AccountID, ExpiresAt: issued.ExpiresAt}, nil
	}, nil
}

// A redirect (particularly 307/308) must not replay an issuer POST with a
// device code or refresh token at a different destination.
func IssuerHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func startGrok(ctx context.Context, transport http.RoundTripper) (Challenge, Poll, error) {
	client := oauth.NewGrokOAuthClient(IssuerHTTPClient(transport))
	code, err := client.RequestDeviceCode(ctx)
	if err != nil {
		return Challenge{}, nil, err
	}
	// RequestDeviceCode calls ValidateGrokDeviceCodeForDisplay before returning.
	challenge := Challenge{VerificationURL: code.VerificationURI, UserCode: code.UserCode}
	return challenge, func(ctx context.Context) (Credentials, error) {
		token, err := client.PollDeviceToken(ctx, code)
		if err != nil {
			return Credentials{}, err
		}
		expires, err := oauth.GrokExpiryUnix(time.Now().Unix(), token.ExpiresIn)
		if err != nil {
			return Credentials{}, err
		}
		subject, err := client.UserInfo(ctx, token.AccessToken)
		if err != nil {
			return Credentials{}, err
		}
		return Credentials{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, AccountID: subject, ExpiresAt: expires}, nil
	}, nil
}
