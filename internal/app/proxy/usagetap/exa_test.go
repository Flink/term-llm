package usagetap

import (
	"strings"
	"testing"
)

func TestExaSearchJSONCost(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		incomplete bool
		want       int64 // -1 means unknown
	}{
		{name: "total only", body: `{"results":[],"costDollars":{"total":0.012,"search":{"neural":0.007}}}`, want: 12000},
		{name: "zero", body: `{"costDollars":{"total":0}}`},
		{name: "rounded", body: `{"costDollars":{"total":2.495e-4}}`, want: 250},
		{name: "missing", body: `{"results":[]}`, want: -1},
		{name: "null", body: `{"costDollars":{"total":null}}`, want: -1},
		{name: "string", body: `{"costDollars":{"total":"0.007"}}`, want: -1},
		{name: "negative", body: `{"costDollars":{"total":-0.007}}`, want: -1},
		{name: "overflow", body: `{"costDollars":{"total":1e20}}`, want: -1},
		{name: "duplicate total", body: `{"costDollars":{"total":0,"total":1}}`, want: -1},
		{name: "duplicate costDollars", body: `{"costDollars":{},"costDollars":{"total":1}}`, want: -1},
		{name: "malformed", body: `{"costDollars":{"total":0}`, want: -1},
		{name: "incomplete", body: `{"costDollars":{"total":0}}`, incomplete: true, want: -1},
		{name: "oversize", body: `{"costDollars":{"total":0}}` + strings.Repeat(" ", MaxEventBytes), want: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tap ExaSearchJSON
			mid := len(tc.body) / 2
			tap.Feed([]byte(tc.body[:mid]))
			tap.Feed([]byte(tc.body[mid:]))
			u := tap.Finish(!tc.incomplete)
			if u.Source != Unknown || u.Input != UnknownTokens || u.Output != UnknownTokens {
				t.Fatalf("Exa cost must not invent token usage: %+v", u)
			}
			if amount := u.ExaCostMicros; (amount == nil) != (tc.want < 0) || (amount != nil && *amount != tc.want) {
				t.Fatalf("wrong estimate: %v (want %d micros, -1 for unknown)", amount, tc.want)
			}
		})
	}
}
