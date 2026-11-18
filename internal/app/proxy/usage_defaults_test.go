package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestVeniceLegacyUsageDefault(t *testing.T) {
	for _, tc := range []struct{ name, upstream, mode, want string }{
		{"legacy", "https://api.venice.ai/api/v1", "", "auto"},
		{"canonical origin", "https://API.VENICE.AI:443/api/v1/", "", "auto"},
		{"explicit off", "https://api.venice.ai/api/v1", "off", "off"},
		{"explicit auto", "https://api.venice.ai/api/v1", "auto", "auto"},
		{"different provider", "https://api.openai.com/v1", "", ""},
		{"different path", "https://api.venice.ai/private", "", ""},
		{"lookalike host", "https://api.venice.ai.example.test/api/v1", "", ""},
		{"insecure", "http://api.venice.ai/api/v1", "", ""},
		{"encoded path", "https://api.venice.ai/api/%761", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Route{Name: "arbitrary-display-name", Upstream: tc.upstream, Usage: tc.mode}
			defaultRouteUsage(&r)
			if r.Usage != tc.want {
				t.Fatalf("usage=%q, want %q", r.Usage, tc.want)
			}
		})
	}
}

func TestVeniceRoutePersistsUsageAndReportedCharge(t *testing.T) {
	const model = "venice-uncensored-1-2"
	const request = `{"model":"` + model + `","stream_options":{"include_usage":true}}`
	const usage = `{"model":"` + model + `","service_tier":"default","usage":{"prompt_tokens":562,"completion_tokens":2,"total_tokens":564,"prompt_tokens_details":{"cached_tokens":544},"cache_read_input_tokens":544`
	const cost = `,"cost":{"usd":0,"diem":0.0002498}`
	for _, tc := range []struct {
		name, billing, mode, cost, wantKind string
		stream                              bool
		status                              int
		wantAmount                          *int64
	}{
		{name: "legacy JSON", cost: cost, wantKind: "charged", wantAmount: moneyPtr(250)},
		{name: "legacy SSE", stream: true, cost: cost, wantKind: "charged", wantAmount: moneyPtr(250)},
		{name: "unspecified zero", billing: "unspecified", cost: `,"cost":{"usd":0,"diem":0}`, wantKind: "charged", wantAmount: moneyPtr(0)},
		{name: "pay as you go", billing: "pay_as_you_go", cost: cost, wantKind: "charged", wantAmount: moneyPtr(250)},
		{name: "no charge evidence", wantKind: "unspecified"},
		{name: "pay as you go without charge", billing: "pay_as_you_go", wantKind: "estimated", wantAmount: moneyPtr(564)},
		{name: "explicit opt-out", mode: "off", cost: cost, wantKind: "unspecified"},
		{name: "explicit plan", billing: "plan", cost: cost, wantKind: "included"},
		{name: "explicit local", billing: "local", cost: cost, wantKind: "none"},
		{name: "HTTP error", status: http.StatusTooManyRequests, cost: cost, wantKind: "unspecified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := usage + tc.cost + "}}"
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			contentType := "application/json"
			if tc.stream {
				body = "data: {\"model\":\"" + model + "\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: " + body + "\n\ndata: [DONE]\n\n"
				contentType = "text/event-stream"
			}
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != request || r.URL.Path != "/api/v1/chat/completions" {
					t.Errorf("request bytes/endpoint changed: %s %s %v", r.URL.Path, got, err)
				}
				if r.Host != "api.venice.ai" || r.Header.Get("Authorization") != "Bearer synthetic-venice-key" {
					t.Error("credential/host substitution changed")
				}
				if tc.mode != "off" && r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("legacy observation did not request identity encoding")
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}))
			defer up.Close()
			// The normal test service has payload capture disabled.
			s := testService(t)
			fixture, _ := url.Parse(up.URL)
			trust := up.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			trust.ServerName = fixture.Hostname()
			s.transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr != "api.venice.ai:443" {
					t.Errorf("unexpected upstream %s", addr)
				}
				return (&tls.Dialer{Config: trust}).DialContext(ctx, network, fixture.Host)
			}
			r := route("custom-name", "/venice/", "https://api.venice.ai/api/v1", "X-Client-Key", "key")
			r.SetHeaders = map[string]HeaderRule{"Authorization": {Secret: "key", Prefix: "Bearer "}}
			r.Usage, r.Billing = tc.mode, tc.billing
			// A matching card would estimate 564 micros, not the actual 250.
			card := MoneyRate{Provider: r.Name, Model: model, Tier: "default", Currency: "USD", Source: "fixture card", AsOf: "2026-11-16", Input: moneyPtr(1000000), Output: moneyPtr(1000000), CacheRead: moneyPtr(1000000)}
			secrets := map[string]material{"key": boundMaterial(t, r.Upstream, "synthetic-venice-key")}
			snap, err := compile(Config{Routes: []Route{r}, Money: MoneySettings{Rates: []MoneyRate{card}}}, s.snap.revision+1, secrets)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.saveConfigAndMoney(snap); err != nil {
				t.Fatal(err)
			}
			s.snap = snap
			token := caller(t, s, r.Name)
			req := httptest.NewRequest("POST", "/venice/chat/completions", strings.NewReader(request))
			req.Header.Set("X-Client-Key", "Local "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != status || w.Body.String() != body {
				t.Fatalf("response changed: %d %s", w.Code, w.Body.String())
			}
			records, _, err := s.store.records(nil, 10)
			if err != nil || len(records) != 1 {
				t.Fatal("missing persisted record", err, len(records))
			}
			rec := records[0]
			if tc.mode == "off" && rec.Usage != nil {
				t.Fatal("explicit opt-out observed usage")
			}
			if tc.mode != "off" && status == http.StatusOK {
				if rec.Usage == nil {
					t.Fatal("Venice returned usage but the legacy route did not persist it")
				}
				u := rec.Usage.Usage
				if u.Input != 562 || u.Output != 2 || u.Total != 564 || u.CachedInput != 544 || u.Model != model || u.Source != usagetap.Reported {
					t.Fatalf("wrong persisted usage: %+v", u)
				}
			}
			wantBilling := tc.billing
			if tc.wantKind == "charged" {
				wantBilling = "pay_as_you_go"
			}
			if wantBilling == "" {
				wantBilling = "unspecified"
			}
			if m := rec.Money; m == nil || m.Kind != tc.wantKind || m.Billing != wantBilling || !reflect.DeepEqual(m.Amount, tc.wantAmount) {
				t.Fatalf("wrong ledger money: %+v, want %s billing, %s kind, amount %v", m, wantBilling, tc.wantKind, tc.wantAmount)
			}
			if s.snap.config.Routes[0].Billing != tc.billing {
				t.Fatal("observation changed configured billing")
			}
			assertVeniceMoneyHistory(t, s, rec)
		})
	}
}

func assertVeniceMoneyHistory(t *testing.T, s *Service, rec Record) {
	t.Helper()
	m := rec.Money
	var raw string
	if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var history MoneyRecord
	if err := json.Unmarshal([]byte(raw), &history); err != nil || !reflect.DeepEqual(history.Money, *m) {
		t.Fatalf("money history differs from ledger: %s (%v)", raw, err)
	}
	// Keep the summary on the request's day, even if the fixture crosses midnight.
	started := time.UnixMilli(rec.Started).UTC()
	now := started.Truncate(24 * time.Hour).Add(24*time.Hour - time.Nanosecond)
	ctx := context.Background()
	summary, err := s.moneySummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []MoneyPeriod{summary.Today, summary.Month} {
		if p.Requests != 1 || (m.Amount == nil && len(p.Totals) != 0) {
			t.Fatalf("unexpected spending coverage: %+v", p)
		}
		if m.Kind == "charged" && (len(p.Totals) != 1 || p.Totals[0].Charged != *m.Amount || p.Totals[0].Total != *m.Amount || p.Totals[0].Estimated != 0 || p.Totals[0].Qualifier != "charged") {
			t.Fatalf("provider charge missing or double counted: %+v", p)
		}
	}
	if m.Kind != "charged" {
		return
	}
	if m.Rate != nil || m.Currency != "USD" || !strings.Contains(m.Source, "Venice") || m.AsOf != time.UnixMilli(rec.Ended).UTC().Format(time.DateOnly) {
		t.Fatalf("wrong reported charge provenance: %+v", m)
	}
	timeline, err := s.moneyTimeline(ctx, now, "7d", "total", nil)
	if err != nil {
		t.Fatal(err)
	}
	day := timeline.Buckets[len(timeline.Buckets)-1]
	wantCosts := []timelineCost{{Currency: "USD", Micros: *m.Amount}}
	if len(day.Series) != 1 || !reflect.DeepEqual(day.Series[0].Costs, wantCosts) || day.Series[0].UnknownCost != 0 {
		t.Fatalf("wrong daily timeline costs: %+v", day)
	}
}
