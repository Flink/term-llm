package usagetap

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExaSearchCostRequiresOfficialEndpoint(t *testing.T) {
	const root = "https://api.exa.ai"
	for _, tc := range []struct {
		name, upstream, endpoint string
		mutate                   func(*http.Response)
		want                     bool
	}{
		{name: "official", upstream: root, endpoint: root + "/search", want: true},
		{name: "explicit port and query", upstream: "https://API.EXA.AI:443/", endpoint: root + "/search?fixture=true", want: true},
		{name: "different upstream", upstream: "https://other.example", endpoint: root + "/search"},
		{name: "different response origin", upstream: root, endpoint: "https://other.example/search"},
		{name: "nondefault port", upstream: root, endpoint: root + ":8443/search"},
		{name: "other endpoint", upstream: root, endpoint: root + "/contents"},
		{name: "encoded path", upstream: root, endpoint: root + "/%73earch"},
		{name: "non-POST", upstream: root, endpoint: root + "/search", mutate: func(r *http.Response) { r.Request.Method = http.MethodGet }},
		{name: "other Host", upstream: root, endpoint: root + "/search", mutate: func(r *http.Response) { r.Request.Host = "other.example" }},
		{name: "HTTP error", upstream: root, endpoint: root + "/search", mutate: func(r *http.Response) { r.StatusCode = http.StatusTooManyRequests }},
		{name: "SSE", upstream: root, endpoint: root + "/search", mutate: func(r *http.Response) { r.Header.Set("Content-Type", "text/event-stream") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Request: httptest.NewRequest(http.MethodPost, tc.endpoint, nil)}
			if tc.mutate != nil {
				tc.mutate(resp)
			}
			if got := IsExaSearchJSON(tc.upstream, resp); got != tc.want {
				t.Fatalf("eligible=%v, want %v", got, tc.want)
			}
		})
	}
}
