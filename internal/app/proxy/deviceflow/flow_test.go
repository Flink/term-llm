package deviceflow

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/term-llm/term-llm/internal/oauth"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGrokDeviceGrant(t *testing.T) {
	const challenge = `{"device_code":"private-device","user_code":"ABC-123","verification_uri":"https://x.ai/device","interval":1,"expires_in":20}`
	for _, action := range []string{"connect", "unsafe-url", "redirect"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls []string
				tr := transportFunc(func(r *http.Request) (*http.Response, error) {
					calls = append(calls, r.URL.String())
					status, headers, body := 200, make(http.Header), ""
					switch r.URL.String() {
					case oauth.GrokDeviceEndpoint:
						body = challenge
						if action == "unsafe-url" {
							body = strings.Replace(body, "https://x.ai", "https://bad.invalid", 1)
						}
					case oauth.GrokTokenEndpoint:
						if r.FormValue("device_code") != "private-device" {
							t.Fatal("poll missing device code")
						}
						body = `{"access_token":"access","refresh_token":"refresh","expires_in":600}`
						if action == "redirect" {
							status = 307
							headers.Set("Location", "https://other.invalid/collect")
						}
					case oauth.GrokUserInfoEndpoint:
						if r.Header.Get("Authorization") != "Bearer access" {
							t.Fatal("userinfo missing authorization")
						}
						body = `{"sub":"fixture-user"}`
					default:
						t.Errorf("unexpected issuer request: %s", r.URL.Host)
						return nil, fmt.Errorf("unexpected origin")
					}
					return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				challenge, poll, err := Start(nil).Request(ctx, ctx, "grok", tr)
				if action == "unsafe-url" {
					if err == nil || poll != nil {
						t.Fatal("accepted unsafe verification URL")
					}
					return
				}
				if err != nil || challenge.VerificationURL != "https://x.ai/device" || challenge.UserCode != "ABC-123" {
					t.Fatalf("challenge = %+v, err = %v", challenge, err)
				}
				creds, err := poll(ctx)
				if action == "redirect" {
					if err == nil || len(calls) != 2 {
						t.Fatalf("redirect replayed device grant: %v", calls)
					}
					return
				}
				if err != nil || creds.AccountID != "fixture-user" || creds.AccessToken != "access" || creds.RefreshToken != "refresh" || creds.ExpiresAt <= time.Now().Unix() {
					t.Fatal("failed device grant or userinfo exchange")
				}
			})
		})
	}
}
