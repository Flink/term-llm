package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestNativeUsageInFlightSurvivesMoneyEdit(t *testing.T) {
	s := testService(t)
	route := usageTestRoute("router", "https://openrouter.ai/api/v1", "", "router-key")
	configure(t, s, Config{Routes: []Route{route}}, map[string]material{"router-key": {Value: "fixture-router-key"}})
	cookie, csrf := loginAdmin(t, s)
	source := s.nativeUsageSources()[0]
	s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(*http.Request) (*http.Response, error) {
		rr := adminRequest(s, "PUT", "money", MoneySettings{Timezone: "UTC"}, cookie, csrf)
		if rr.Code != 200 {
			t.Fatalf("unrelated config edit: %d %s", rr.Code, rr.Body.String())
		}
		return nativeUsageFixture(200, `{"data":{"is_management_key":false,"usage":4,"limit":10,"limit_remaining":6}}`), nil
	}))
	report, err := source.Fetch(t.Context())
	if err != nil || len(report.Groups) != 1 || report.Groups[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("in-flight read lost after unrelated edit: %+v, %v", report, err)
	}
	route.Enabled = false
	configure(t, s, Config{Routes: []Route{route}}, map[string]material{"router-key": {Value: "fixture-router-key"}})
	if _, err := source.Fetch(t.Context()); !errors.Is(err, errNativeUsageChanged) {
		t.Fatalf("disabled binding still admitted: %v", err)
	}
}

func TestNativeUsageDistinctAccountsPolledIndependently(t *testing.T) {
	s := testService(t)
	var routes []Route
	for _, name := range []string{"a", "b", "a-alias"} {
		account := name
		if name == "a-alias" {
			account = "a"
		} else {
			usageTestAccount(t, s, "chatgpt", account, "fixture-access-"+account)
		}
		r := usageTestRoute(name, "https://chatgpt.com/backend-api/codex", account, "")
		r.Provider, r.Profile = "chatgpt", "chatgpt"
		if name != "a" {
			r.Profile, r.Upstream = "chatgpt-backend", "https://chatgpt.com/backend-api"
		}
		routes = append(routes, r)
	}
	configure(t, s, Config{Routes: routes}, nil)
	sources := s.nativeUsageSources()
	if len(sources) != 2 || sources[0].ID != sources[1].ID || sources[0].Key == sources[1].Key || sources[0].Route == sources[1].Route {
		t.Fatalf("wanted independent entries for exactly two bound credentials; count=%d", len(sources))
	}
	s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("ChatGPT-Account-ID") == "identity-a" {
			return nativeUsageFixture(403, ""), nil
		}
		return nativeUsageFixture(200, `{"rate_limit":{"primary_window":{"used_percent":42,"limit_window_seconds":18000}}}`), nil
	}))
	var ok, failed int
	for _, source := range sources {
		report, err := source.Fetch(context.Background())
		if err != nil {
			failed++
			continue
		}
		if len(report.Groups) != 1 || report.Groups[0].Windows[0].UsedPercent != 42 {
			t.Fatal("lost valid sibling report")
		}
		ok++
	}
	if failed != 1 || ok != 1 {
		t.Fatalf("success=%d failure=%d", ok, failed)
	}
}
