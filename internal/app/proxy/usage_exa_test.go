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
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestExaRoutePersistsUsageAndReportedEstimate(t *testing.T) {
	const withCost = "{\n  \"results\": [], \"costDollars\": {\"total\": 0.012}\n}\n"
	for _, tc := range []struct {
		name, usage, body string
		wantEstimate      bool
	}{
		{name: "legacy route", body: withCost, wantEstimate: true},
		{name: "explicit opt-out", usage: "off", body: withCost},
		{name: "missing cost", body: `{"results":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, token := reopenExistingExaRoute(t, tc.usage)
			const request = `{"query": "fixture search"}`
			stubExaSearch(t, s, request, tc.body)
			req := httptest.NewRequest(http.MethodPost, "/search-proxy/search", strings.NewReader(request))
			req.Header.Set("X-Client-Key", "Local "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != tc.body {
				t.Fatalf("response changed: got %d %q, want %d %q", w.Code, w.Body.String(), http.StatusOK, tc.body)
			}
			records, _, err := s.store.records(nil, 10)
			if err != nil || len(records) != 1 {
				t.Fatalf("expected one persisted request: got %d (%v)", len(records), err)
			}
			observed := records[0].Usage
			switch {
			case tc.wantEstimate:
				if observed == nil || observed.Protocol != usagetap.ExaSearch || observed.Usage.ExaCostMicros == nil || *observed.Usage.ExaCostMicros != 12000 {
					t.Fatalf("expected persisted Exa cost evidence: %+v", observed)
				}
			case tc.usage == "off":
				if observed != nil {
					t.Fatalf("explicit opt-out observed usage: %+v", observed)
				}
			}
			assertExaSpending(t, s, records[0], tc.wantEstimate)
		})
	}
}

const exaFixtureKey = "synthetic-exa-key"

func reopenExistingExaRoute(t *testing.T, usage string) (*Service, string) {
	t.Helper()
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	if w := adminRequest(s, http.MethodPut, "secrets", map[string]string{"name": "key", "value": exaFixtureKey}, cookie, csrf); w.Code != http.StatusOK {
		t.Fatalf("persist route credential: %d %s", w.Code, w.Body)
	}
	r := route("custom-search", "/search-proxy/", "https://api.exa.ai", "X-Client-Key", "key")
	r.SetHeaders = map[string]HeaderRule{"X-Api-Key": {Secret: "key"}}
	r.Billing, r.Usage = "pay_as_you_go", usage
	c := fixtureProviders(Config{Routes: []Route{r}})
	configure(t, s, c, s.snap.secrets)
	token := caller(t, s, r.Name)
	// Simulate the pre-upgrade configuration row. Saving through configure
	// could already normalize the unset usage mode, defeating the reload test.
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	execProxySQL(t, s.store, "UPDATE configuration SET data=? WHERE id=1", string(raw))
	opts := s.opts
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(opts)
	if err != nil {
		t.Fatalf("reload persisted route: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	return reopened, token
}

func stubExaSearch(t *testing.T, s *Service, request, response string) {
	t.Helper()
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != request || r.Method != http.MethodPost || r.URL.RequestURI() != "/search" {
			t.Errorf("upstream request changed: %s %s %q (%v)", r.Method, r.URL.RequestURI(), got, err)
		}
		if r.Host != "api.exa.ai" || r.Header.Get("X-Api-Key") != exaFixtureKey || r.Header.Get("X-Client-Key") != "" {
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
		if addr != "api.exa.ai:443" {
			return nil, fmt.Errorf("unexpected TLS upstream dial: %s", addr)
		}
		return (&tls.Dialer{Config: trust}).DialContext(ctx, network, fixtureAddr)
	}
}

func assertExaSpending(t *testing.T, s *Service, rec Record, wantEstimate bool) {
	t.Helper()
	const micros = 12000
	m := rec.Money
	if m == nil || m.Billing != "pay_as_you_go" {
		t.Fatalf("expected a pay-as-you-go ledger entry: %+v", m)
	}
	if wantEstimate {
		if m.Kind != "estimated" || m.Amount == nil || *m.Amount != micros || m.Currency != "USD" || !strings.Contains(strings.ToLower(m.Source), "exa") {
			t.Fatalf("expected an Exa estimate of %d USD micros: %+v", micros, m)
		}
	} else if m.Amount != nil {
		t.Fatalf("unexpected monetary amount without usable Exa cost evidence: %+v", m)
	}

	var raw string
	if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var history MoneyRecord
	if err := json.Unmarshal([]byte(raw), &history); err != nil || !reflect.DeepEqual(history.Money, *m) {
		t.Fatalf("money history differs from ledger: %s (%v)", raw, err)
	}
	// Anchor the summary to the request's UTC day, even across midnight.
	now := time.UnixMilli(rec.Started).UTC().Truncate(24 * time.Hour).Add(24*time.Hour - time.Nanosecond)
	summary, err := s.moneySummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []MoneyPeriod{summary.Today, summary.Month} {
		if p.Requests != 1 {
			t.Fatalf("expected exactly one request in spending summary: %+v", p)
		}
		if !wantEstimate {
			if len(p.Totals) != 0 {
				t.Fatalf("unexpected spending without usable Exa cost evidence: %+v", p)
			}
			continue
		}
		want := []MoneyTotal{{Currency: "USD", Estimated: micros, Total: micros, Qualifier: "about", Sources: []MoneyPriceSource{{Source: m.Source, AsOf: m.AsOf, Kind: "estimated"}}}}
		if !reflect.DeepEqual(p.Totals, want) {
			t.Fatalf("wrong Exa spending totals: got %+v, want %+v", p.Totals, want)
		}
	}
}
