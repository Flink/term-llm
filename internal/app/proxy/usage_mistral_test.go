package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestMistralRoutePersistsUsageAndCatalogPrice(t *testing.T) {
	const root = "https://api.mistral.ai/v1"
	const model = "mistral-fixture-2501"
	const key = "synthetic-mistral-key"
	const identity = `"id":"fixture-chat","model":"` + model + `"`
	const usage = `"usage":{"prompt_tokens":407,"completion_tokens":63,"total_tokens":470,"service_tier":"standard"}`
	// The Mistral catalog entry intentionally has no API URL or cache rates.
	const catalog = `{"mistral":{"models":{"` + model + `":{"cost":{"input":0.3,"output":0.8}}}}}`

	preset, ok := routePresetByID("mistral")
	if !ok || preset.Upstream != root || preset.Usage != "auto" {
		t.Fatalf("Mistral preset missing canonical observation defaults: %+v", preset)
	}
	for _, tc := range []struct {
		name                    string
		preset, stream, disable bool
	}{
		{name: "preset JSON", preset: true},
		{name: "legacy unset SSE", stream: true},
		{name: "legacy explicit off SSE", stream: true, disable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := fmt.Sprintf(`{"model":"mistral-fixture-latest", "messages":[{"role":"user","content":"fixture"}], "stream":%t}`, tc.stream)
			body := "{" + identity + `,"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],` + usage + "}\n"
			contentType := "application/json"
			if tc.stream {
				chunk := "data: {" + identity + `,"object":"chat.completion.chunk",`
				body = chunk + `"choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}` + "\n\n" +
					chunk + `"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` + usage + "}\n\ndata: [DONE]\n\n"
				contentType = "text/event-stream"
			}

			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != request || r.Method != http.MethodPost || r.URL.RequestURI() != "/v1/chat/completions" {
					t.Errorf("forwarded request differs: %s %s %q (%v)", r.Method, r.URL, got, err)
				}
				if r.Host != "api.mistral.ai" || r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("upstream host or credential substitution differs")
				}
				if !tc.disable && r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("usage observation did not negotiate identity encoding")
				}
				w.Header().Set("Content-Type", contentType)
				_, _ = io.WriteString(w, body)
			}))
			defer up.Close()

			s := testService(t)
			s.pricing = proxyCatalog(t, catalog)
			// Preserve the canonical request authority without ever dialing it.
			s.transport.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
				return nil, fmt.Errorf("unexpected non-TLS upstream %s", addr)
			}
			s.transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr != "api.mistral.ai:443" {
					return nil, fmt.Errorf("unexpected upstream %s", addr)
				}
				trust := up.Client().Transport.(*http.Transport).TLSClientConfig
				return (&tls.Dialer{Config: trust}).DialContext(ctx, network, up.Listener.Addr().String())
			}
			r := Route{Name: "fixture-chat", LocalPrefix: "/fixture-chat/", Upstream: root, Enabled: true,
				CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "},
				SetHeaders: map[string]HeaderRule{"Authorization": {Secret: "key", Prefix: "Bearer "}}, Billing: "pay_as_you_go"}
			if tc.preset {
				r = preset.newRoute("fixture-provider", r.Name, "key")
			}
			if tc.disable {
				r.Usage = "off"
			}
			configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, r.Upstream, key)})
			req := httptest.NewRequest(http.MethodPost, r.LocalPrefix+"chat/completions", strings.NewReader(request))
			req.Header.Set("Authorization", "Bearer "+caller(t, s, r.Name))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != contentType || w.Body.String() != body {
				t.Fatalf("forwarded response differs: %d %s %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}

			records, _, err := s.store.records(nil, 10)
			if err != nil || len(records) != 1 {
				t.Fatalf("expected one persisted request, got %d (%v)", len(records), err)
			}
			rec := records[0]
			m := rec.Money
			if m == nil || m.Billing != "pay_as_you_go" {
				t.Fatalf("wrong persisted billing: %+v", m)
			}
			if tc.disable {
				if rec.Usage != nil || m.Kind != "unmeasured" || m.Amount != nil || m.Rate != nil {
					t.Fatalf("explicit opt-out recorded usage or pricing: usage=%+v, money=%+v", rec.Usage, m)
				}
			} else {
				if u := rec.Usage; u == nil || u.Protocol != usagetap.ChatCompletions || u.Usage.Source != usagetap.Reported ||
					u.Usage.Model != model || u.Usage.Tier != "standard" || u.Usage.Input != 407 || u.Usage.Output != 63 ||
					u.Usage.Total != 470 || u.Usage.CachedInput != 0 || u.Usage.Turns != 1 || u.Usage.Ambiguous {
					t.Fatalf("wrong persisted usage: %+v", u)
				}
				// 407 * 0.30 + 63 * 0.80 = 172.5 micros, rounded up once.
				if m.Kind != "estimated" || m.Amount == nil || *m.Amount != 173 || m.Currency != "USD" || m.Source != "models.dev/mistral" || m.AsOf == "" {
					t.Fatalf("wrong persisted estimate (want 173 USD micros from models.dev/mistral): %+v", m)
				}
				if rate := m.Rate; rate == nil || rate.Model != model || rate.Tier != "standard" ||
					!reflect.DeepEqual(rate.Input, moneyPtr(300000)) || !reflect.DeepEqual(rate.Output, moneyPtr(800000)) {
					t.Fatalf("wrong persisted catalog rate: %+v", rate)
				}
			}
			var raw string
			if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var history MoneyRecord
			if err := json.Unmarshal([]byte(raw), &history); err != nil || !reflect.DeepEqual(history.Money, *m) || !reflect.DeepEqual(history.Usage, rec.Usage) {
				t.Fatalf("money history differs from persisted request: %s (%v)", raw, err)
			}
		})
	}
}
