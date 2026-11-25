package mcpweb

import (
	"net/url"
	"reflect"
	"testing"
)

func TestParseOAuthCallbackQuery(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want url.Values
	}{
		{
			name: "authorization code",
			raw:  "code=abc%2Bdef&state=test-state&iss=https%3A%2F%2Fissuer.example&ignored=one&ignored=two",
			want: url.Values{"code": {"abc+def"}, "state": {"test-state"}, "iss": {"https://issuer.example"}},
		},
		{
			name: "authorization denied",
			raw:  "state=test-state&error=access_denied&error_description=ignored",
			want: url.Values{"state": {"test-state"}, "error": {"access_denied"}},
		},
		{
			name: "empty optional issuer",
			raw:  "code=test-code&state=test-state&iss=",
			want: url.Values{"code": {"test-code"}, "state": {"test-state"}, "iss": {""}},
		},
		{name: "malformed escape", raw: "code=test-code&state=test-state&ignored=%ZZ"},
		{name: "malformed separator", raw: "code=test-code&state=test-state&ignored=one;two"},
		{name: "duplicate code", raw: "code=test-code&code=test-code&state=test-state"},
		{name: "duplicate state after decoding", raw: "code=test-code&state=test-state&%73tate=other"},
		{name: "duplicate issuer", raw: "code=test-code&state=test-state&iss=&iss="},
		{name: "duplicate error", raw: "state=test-state&error=access_denied&error=access_denied"},
		{name: "missing state", raw: "code=test-code"},
		{name: "empty state", raw: "code=test-code&state="},
		{name: "missing code and error", raw: "state=test-state"},
		{name: "empty code", raw: "code=&state=test-state"},
		{name: "empty error", raw: "error=&state=test-state"},
		{name: "code and error", raw: "code=test-code&error=access_denied&state=test-state"},
		{name: "code and empty error", raw: "code=test-code&error=&state=test-state"},
		{name: "error and empty code", raw: "code=&error=access_denied&state=test-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseOAuthCallbackQuery(tc.raw)
			if tc.want == nil {
				if err == nil || got != nil {
					t.Fatal("invalid callback did not fail closed")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsed callback = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
