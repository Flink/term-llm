package proxy

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func TestCallerTagAttribution(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Local") != "" {
			t.Error("caller token reached the upstream")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	s := testService(t)
	configure(t, s, Config{Routes: []Route{route("api", "/api/", upstream.URL, "X-Local", "vendor")}}, map[string]material{
		"vendor": boundMaterial(t, upstream.URL, "fixture-vendor"),
	})
	caller, secret, err := s.store.createCaller("Name", []string{"api"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	tagged := tagFixtureCaller(t, secret)
	for _, token := range []string{secret, tagged} {
		if rr := call(s, "GET", "/api/resource", "X-Local", token, nil); rr.Code != http.StatusNoContent {
			t.Fatalf("forward status = %d", rr.Code)
		}
	}
	unknown := tagFixtureCaller(t, randomToken("sk-tlp-"))
	if rr := call(s, "GET", "/api/resource", "X-Local", unknown, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unknown tagged caller status = %d", rr.Code)
	}
	if rr := call(s, "GET", "/api/resource", "X-Local", secret+"._invalid", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("invalid tag status = %d", rr.Code)
	}

	records, _, err := s.store.records(nil, 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("record count = %d, err = %v", len(records), err)
	}
	for i, tag := range []string{"abcd", ""} {
		rec := records[i]
		if rec.CallerID != caller.ID || rec.CallerName != "Name" || rec.Tag != tag || rec.Outcome != "complete" {
			t.Fatalf("unexpected attribution: %+v", rec)
		}
		var data []byte
		if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var money MoneyRecord
		if err := json.Unmarshal(data, &money); err != nil || money.Tag != tag || money.DeviceID != caller.ID {
			t.Fatalf("unexpected persisted money attribution: %+v, %v", money, err)
		}
	}
	turn := webSocketTurnRecord(records[0], usagetap.WebSocketTurn{Ended: time.Now()})
	if turn.Tag != "abcd" || requestMoneyRecord(turn).Tag != "abcd" {
		t.Fatal("WebSocket turn lost the connection's attribution")
	}
	path := "/" + url.PathEscape(tagged) + "/" + url.PathEscape(secret)
	if result := s.redactPath(path, tagged, s.snap); result != "/[redacted]/[redacted]" {
		t.Fatal("diagnostic path did not redact both forms of the token")
	}
	assertCallerTagTimeline(t, s, caller.ID)
}

func assertCallerTagTimeline(t *testing.T, s *Service, callerID string) {
	t.Helper()
	now := time.Now()
	series, err := s.moneyTimeline(context.Background(), now, "7d", "client", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, bucket := range series.Buckets {
		for _, group := range bucket.Series {
			labels[group.Key] = group.Label
		}
	}
	if want := (map[string]string{callerID: "Name", callerID + "\x00abcd": "Name · abcd"}); !maps.Equal(labels, want) {
		t.Fatalf("unexpected timeline groups: %v", labels)
	}
	summary, err := s.moneySummary(context.Background(), now)
	if err != nil || len(summary.Devices) != 1 || summary.Devices[0].Name != "Name" {
		t.Fatalf("tag split the per-caller Devices summary: %+v, %v", summary.Devices, err)
	}
}

func TestCallerTagClientQueries(t *testing.T) {
	s := testService(t)
	configure(t, s, Config{Routes: []Route{{Name: "api", LocalPrefix: "/api/", Upstream: "https://api.example.test", Enabled: true, CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}}}}, nil)
	tagged := tagFixtureCaller(t, caller(t, s, "api"))
	for endpoint, want := range map[string]string{
		"v1/whoami":    `"name":"test caller"`,
		"v2/providers": `"id":"api"`,
	} {
		t.Run(endpoint, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/_proxy/client/"+endpoint, nil)
			r.Header.Set("Authorization", "Bearer "+tagged)
			rr := httptest.NewRecorder()
			s.ServeHTTP(rr, r)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), want) || strings.Contains(rr.Body.String(), "abcd") {
				t.Fatalf("client lookup did not return the underlying caller/grants: status %d", rr.Code)
			}
		})
	}
}

func TestCallerTagClaudeNativeForward(t *testing.T) {
	f := newClaudeForwardFixture(t)
	tagged := tagFixtureCaller(t, f.token)
	if bootstrap := f.bootstrap(t, tagged); bootstrap.AccessPlaceholder != f.access {
		t.Fatal("tag changed the caller-scoped Claude access alias")
	}
	conn, reader := f.nativeHost(t, claudeAPIHost, tagged)
	if code, _ := claudeForwardNativeCall(t, conn, reader, "POST", "/v1/messages", claudeAPIHost, f.access, ""); code != 200 {
		t.Fatalf("tagged native inference status = %d", code)
	}
	recs, _, err := f.service.store.records(nil, 10)
	if err != nil || len(recs) != 1 || recs[0].Tag != "abcd" || recs[0].CallerName != "test caller" {
		t.Fatalf("native request did not retain caller attribution: %v", err)
	}
}

func tagFixtureCaller(t *testing.T, secret string) string {
	t.Helper()
	token, err := proxyprofile.TagCallerToken(secret, "abcd")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
