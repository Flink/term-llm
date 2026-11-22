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

const (
	typeSafeFixtureKey     = "synthetic-typesafe-key"
	typeSafeFixtureHost    = "api.typesafe.ai"
	typeSafeSystemOnePath  = "/v1/systemone"
	typeSafeFixtureRequest = `{"model":"jev-latest","state":"fixture","questions":{}}`
)

func TestTypeSafeSystemOneRouteRecordsCostAfterInPlaceEdit(t *testing.T) {
	for _, tc := range []struct {
		name, body, model, kind string
	}{
		{
			name:  "known model",
			body:  "{\n  \"model\":\"jev-1.14.0\", \"answers\":{}, \"usage\":{\"input_tokens\":10000,\"output_tokens\":300}\n}\n",
			model: "jev-1.14.0",
			kind:  "estimated",
		},
		{
			name:  "unknown model",
			body:  `{"model":"jev-unknown","answers":{},"usage":{"input_tokens":10000,"output_tokens":300}}`,
			model: "jev-unknown",
			kind:  "unpriced",
		},
		{
			name: "missing counts",
			body: `{"model":"jev-1.14.0","answers":{},"usage":{}}`,
			kind: "unmeasured",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			w := adminRequest(s, http.MethodPost, "providers", providerCreateInput{
				Revision: s.snap.revision, Name: "typesafe", Preset: "typesafe", Value: typeSafeFixtureKey,
			}, cookie, csrf)
			if w.Code != http.StatusCreated {
				t.Fatalf("create TypeSafe provider: %d %s", w.Code, w.Body)
			}
			c := s.snap.config
			r := c.Routes[0]
			r.Billing, r.Usage = "unspecified", "off"
			edit := func() {
				t.Helper()
				w := adminRequest(s, http.MethodPut, "config", map[string]any{
					"revision": s.snap.revision, "routes": []Route{r}, "providers": c.Providers,
				}, cookie, csrf)
				if w.Code != http.StatusOK {
					t.Fatalf("edit existing TypeSafe route: %d %s", w.Code, w.Body)
				}
			}
			edit()
			token := caller(t, s, r.Name)
			stubTypeSafeSystemOne(t, s, tc.body)

			legacy := postTypeSafeSystemOne(t, s, r, token, tc.body, 1)
			if legacy.Usage != nil || legacy.Money == nil || legacy.Money.Billing != "unspecified" || legacy.Money.Amount != nil {
				t.Fatalf("legacy route must not observe usage or record an amount: usage=%+v money=%+v", legacy.Usage, legacy.Money)
			}
			r.Billing, r.Usage = "pay_as_you_go", "auto"
			edit()
			rec := postTypeSafeSystemOne(t, s, r, token, tc.body, 2) // Same route and caller grant.
			if u := rec.Usage; tc.model != "" && (u == nil || u.Usage.Model != tc.model || u.Usage.Source != usagetap.Reported || u.Usage.Input != 10000 || u.Usage.Output != 300) {
				t.Fatalf("expected persisted usage for %s with 10000 input and 300 output tokens: %+v", tc.model, u)
			}
			assertTypeSafeMoney(t, s, rec, tc.kind)
		})
	}
}

func postTypeSafeSystemOne(t *testing.T, s *Service, r Route, token, response string, wantRecords int) Record {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, strings.TrimSuffix(r.LocalPrefix, "/")+typeSafeSystemOnePath, strings.NewReader(typeSafeFixtureRequest))
	req.Header.Set(r.CallerAuth.Header, r.CallerAuth.Prefix+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != response {
		t.Fatalf("response changed: got %d %s %q, want 200 application/json %q", w.Code, w.Header().Get("Content-Type"), w.Body.String(), response)
	}
	records, _, err := s.store.records(nil, 10)
	if err != nil || len(records) != wantRecords {
		t.Fatalf("expected %d persisted requests: got %d (%v)", wantRecords, len(records), err)
	}
	return records[0]
}

func assertTypeSafeMoney(t *testing.T, s *Service, rec Record, kind string) {
	t.Helper()
	m := rec.Money
	if m == nil || m.Billing != "pay_as_you_go" || m.Kind != kind {
		t.Fatalf("expected a pay-as-you-go %s ledger entry: %+v", kind, m)
	}
	if kind == "estimated" {
		// 10,000 input tokens at $0.042 per million; output tokens are free.
		if m.Amount == nil || *m.Amount != 420 || m.Currency != "USD" {
			t.Fatalf("expected an input-only estimate of 420 USD micros: %+v", m)
		}
	} else if m.Amount != nil {
		t.Fatalf("unexpected amount without a usable model and counts: %+v", m)
	}
	var raw string
	if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var history MoneyRecord
	if err := json.Unmarshal([]byte(raw), &history); err != nil || !reflect.DeepEqual(history.Money, *m) {
		t.Fatalf("money history differs from ledger: %s (%v)", raw, err)
	}
}

func stubTypeSafeSystemOne(t *testing.T, s *Service, response string) {
	t.Helper()
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != typeSafeFixtureRequest || r.Method != http.MethodPost || r.URL.RequestURI() != typeSafeSystemOnePath {
			t.Errorf("upstream request changed: %s %s %q (%v)", r.Method, r.URL.RequestURI(), got, err)
		}
		if r.Host != typeSafeFixtureHost || r.Header.Get("Authorization") != "Bearer "+typeSafeFixtureKey {
			t.Error("upstream host or credential substitution changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(up.Close)
	fixtureAddr := up.Listener.Addr().String()
	fixtureHost, _, err := net.SplitHostPort(fixtureAddr)
	if err != nil {
		t.Fatal(err)
	}
	trust := up.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	trust.ServerName = fixtureHost
	s.transport.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("unexpected plaintext upstream dial: %s", addr)
	}
	s.transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != typeSafeFixtureHost+":443" {
			return nil, fmt.Errorf("unexpected TLS upstream dial: %s", addr)
		}
		return (&tls.Dialer{Config: trust}).DialContext(ctx, network, fixtureAddr)
	}
}

func TestTypeSafePriceCardOverridesBundledRate(t *testing.T) {
	const model = "jev-1.13.0"
	route := Route{Name: "my-typesafe", Upstream: "https://api.typesafe.ai", Billing: "pay_as_you_go"}
	tap := usagetap.New(usagetap.TypeSafeSystemOne, usagetap.JSON)
	tap.Feed([]byte(`{"model":"jev-1.13.0","usage":{"input_tokens":10000,"output_tokens":300}}`))
	observed := ObservedUsage{Protocol: usagetap.TypeSafeSystemOne, Usage: tap.Finish(true)}
	in := moneyInputFromUsage(MoneyInput{Billing: route.Billing, Provider: route.Name}, observed)
	card := MoneyRate{Provider: route.Name, Model: model, Currency: "USD", Source: "operator", AsOf: "2026-11-22", Input: moneyPtr(84000), Output: moneyPtr(0)}
	for _, tc := range []struct {
		cards []MoneyRate
		want  int64
	}{
		{nil, 420},
		{[]MoneyRate{card}, 840},
	} {
		m := finishRequestMoney(in, route, &observed, tc.cards)
		if m.Kind != "estimated" || m.Amount == nil || *m.Amount != tc.want || m.Rate == nil || m.Rate.Model != model {
			t.Fatalf("expected %d USD micros with model rate snapshot, got %+v", tc.want, m)
		}
	}
}
