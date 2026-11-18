package proxy

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestUsageObserverVeniceChargeGate(t *testing.T) {
	const root = "https://api.venice.ai/api/v1"
	const endpoint = root + "/chat/completions"
	const payload = `{"usage":{"prompt_tokens":1204,"cost":{"usd":0,"diem":0.0002498}}}`
	for _, tc := range []struct {
		name, upstream, responseURL string
		change                      func(*Route, *http.Response)
		charge                      bool
	}{
		{"canonical", root, endpoint, nil, true},
		{"canonical with query", "https://API.VENICE.AI:443/api/v1/", endpoint + "?x=1", nil, true},
		{"SSE", root, endpoint, func(_ *Route, r *http.Response) {
			r.Header.Set("Content-Type", "text/event-stream")
			r.Body = io.NopCloser(strings.NewReader("data: " + payload + "\n\ndata: [DONE]\n\n"))
		}, true},
		{"missing content type", root, endpoint, func(_ *Route, r *http.Response) { r.Header.Del("Content-Type") }, true},
		{"other provider", "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1/chat/completions", nil, false},
		{"lookalike root", root + "/extra", endpoint, nil, false},
		{"lookalike host", "https://api.venice.ai.test/api/v1", "https://api.venice.ai.test/api/v1/chat/completions", nil, false},
		{"redirect to Venice from other provider", "https://openrouter.ai/api/v1", endpoint, nil, false},
		{"redirect to foreign origin", root, "https://other.invalid/api/v1/chat/completions", nil, false},
		{"redirect to foreign path", root, "https://api.venice.ai/api/v2/chat/completions", nil, false},
		{"redirect to HTTP", root, "http://api.venice.ai/api/v1/chat/completions", nil, false},
		{"redirect to other port", root, "https://api.venice.ai:8443/api/v1/chat/completions", nil, false},
		{"alternate Host", root, endpoint, func(_ *Route, r *http.Response) { r.Request.Host = "other.invalid" }, false},
		{"escaped path", root, root + "/%63hat/completions", nil, false},
		{"nonchat endpoint", root, root + "/images/generations", nil, false},
		{"missing origin", root, "/api/v1/chat/completions", nil, false},
		{"missing request", root, "", nil, false},
		{"redirect response", root, endpoint, func(_ *Route, r *http.Response) { r.StatusCode = 302 }, false},
		{"error response", root, endpoint, func(_ *Route, r *http.Response) { r.StatusCode = 429 }, false},
		{"compressed", root, endpoint, func(_ *Route, r *http.Response) { r.Header.Set("Content-Encoding", "gzip") }, false},
		{"automatically decompressed", root, endpoint, func(_ *Route, r *http.Response) { r.Uncompressed = true }, false},
		{"unsupported media", root, endpoint, func(_ *Route, r *http.Response) { r.Header.Set("Content-Type", "text/plain") }, false},
		{"ambiguous media", root, endpoint, func(_ *Route, r *http.Response) { r.Header.Add("Content-Type", "text/plain") }, false},
		{"NDJSON", root, endpoint, func(_ *Route, r *http.Response) { r.Header.Set("Content-Type", "application/x-ndjson") }, false},
		{"opt-out", root, endpoint, func(route *Route, _ *http.Response) { route.Usage = "off" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := Route{Name: "venice", Usage: "auto", Upstream: tc.upstream}
			resp := usageResponse("/api/v1/chat/completions", "application/json", io.NopCloser(strings.NewReader(payload)))
			if tc.responseURL == "" {
				resp.Request = nil
			} else {
				u, err := url.Parse(tc.responseURL)
				if err != nil {
					t.Fatal(err)
				}
				resp.Request.URL, resp.Request.Host = u, u.Host
			}
			if tc.change != nil {
				tc.change(&route, resp)
			}
			var got ObservedUsage
			NewUsageObserver(route, resp, func(u ObservedUsage) { got = u })
			out, err := io.ReadAll(resp.Body)
			if err != nil || !strings.Contains(string(out), payload) {
				t.Fatalf("response changed: %q (%v)", out, err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if c := got.Usage.VeniceCostMicros; (c != nil) != tc.charge || (c != nil && *c != 250) {
				t.Fatalf("usage = %+v; want Venice charge: %t", got.Usage, tc.charge)
			}
			if (tc.charge || tc.name == "other provider") && (got.Protocol != usagetap.ChatCompletions || got.Usage.Input != 1204 || got.Usage.Source != usagetap.Reported) {
				t.Fatalf("expected token evidence independently: %+v", got)
			}
		})
	}
}

func TestUsageObserverVeniceChargeDiscardsCanceledStream(t *testing.T) {
	const root = "https://api.venice.ai/api/v1"
	resp := usageResponse("/api/v1/chat/completions", "text/event-stream", io.NopCloser(strings.NewReader("data: {\"usage\":{\"prompt_tokens\":1,\"cost\":{\"diem\":1}}}\n\ndata: [DONE]\n\n")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Request = req
	var got ObservedUsage
	NewUsageObserver(Route{Usage: "auto", Upstream: root}, resp, func(u ObservedUsage) { got = u })
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if got.Usage.VeniceCostMicros != nil || got.Usage.Input != 1 || got.Usage.Source != usagetap.Partial {
		t.Fatalf("canceled stream must not report a charge: %+v", got)
	}
}
