package usagetap

import (
	"fmt"
	"strings"
	"testing"
)

func TestMissingContentTypeFraming(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_test","model":"test-model","usage":{"input_tokens":18,"output_tokens":5,"total_tokens":23}}}`
	for _, body := range []string{
		completed,
		" \n" + completed,
		"event: response.completed\ndata: " + completed + "\n\n",
		"data: " + completed + "\n\n",
		": heartbeat\n\nevent: response.completed\ndata: " + completed + "\n\n",
		"id: first\nretry: 1000\ndata: " + completed + "\n\n",
	} {
		for _, chunk := range []int{1, 3, len(body)} {
			t.Run(fmt.Sprintf("%q/chunk=%d", body[:min(len(body), 24)], chunk), func(t *testing.T) {
				p := ForResponse("/backend-api/codex/responses", "")
				for i := 0; i < len(body); i += chunk {
					p.Feed([]byte(body[i:min(i+chunk, len(body))]))
				}
				u := p.Finish(true)
				if u.Source != Reported || u.Input != 18 || u.Output != 5 || u.Total != 23 || u.Model != "test-model" {
					t.Fatalf("usage = %+v", u)
				}
			})
		}
	}
}

func TestMissingContentTypeFramingFailsClosed(t *testing.T) {
	for _, body := range []string{"", "e", "<html>not a response</html>", strings.Repeat(" ", maxFramingPrefix) + responseJSON, "not SSE\ndata: " + responseJSON + "\n\n"} {
		p := ForResponse("/responses", "")
		p.Feed([]byte(body))
		if u := p.Finish(true); u.Source != Unknown || u.Input != UnknownTokens {
			t.Fatalf("body %q: %+v", body, u)
		}
		if len(p.buf) > maxFramingPrefix {
			t.Fatal("unbounded framing prefix")
		}
	}
	p := ForResponse("/responses", "application/octet-stream")
	p.Feed([]byte("data: " + responseJSON + "\n\n"))
	if u := p.Finish(true); u.Source != Unknown {
		t.Fatal("explicit unsupported type must stay unknown", u)
	}
}

func TestMissingContentTypeInterruptedSSEPreservesPartialUsage(t *testing.T) {
	p := ForResponse("/responses", "")
	p.Feed([]byte("event: response.completed\ndata: " + responseJSON + "\n\n"))
	if u := p.Finish(false); u.Source != Partial || u.Input == UnknownTokens {
		t.Fatal(u)
	}
}
