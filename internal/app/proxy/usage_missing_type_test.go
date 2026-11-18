package proxy

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestUsageObserverMissingContentType(t *testing.T) {
	for _, body := range []string{observedResponseJSON, "event: response.completed\ndata: " + observedResponseJSON + "\n\n"} {
		for _, canceled := range []bool{false, true} {
			if canceled && strings.HasPrefix(body, "{") {
				continue // Buffered JSON is deliberately discarded on cancellation.
			}
			resp := usageResponse("/backend-api/codex/responses", "", io.NopCloser(strings.NewReader(body)))
			ctx, cancel := context.WithCancel(context.Background())
			resp.Request = resp.Request.WithContext(ctx)
			var got ObservedUsage
			NewUsageObserver(Route{Usage: "auto"}, resp, func(u ObservedUsage) { got = u })
			forwarded, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if canceled {
				// Native clients may close immediately after response.completed.
				// Keep the reported counts, without claiming transport completion.
				cancel()
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			cancel()
			wantSource := usagetap.Reported
			if canceled {
				wantSource = usagetap.Partial
			}
			if got.Protocol != usagetap.Responses || got.Usage.Source != wantSource || got.Usage.Input != 12 || got.Usage.Output != 7 || got.Usage.Total != 19 {
				t.Fatalf("canceled=%t: %+v", canceled, got)
			}
			if string(forwarded) != body || resp.Header.Get("Content-Type") != "" {
				t.Fatal("observing missing media type must not rewrite the response")
			}
		}
	}
}
