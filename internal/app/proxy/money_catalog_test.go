package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
	"github.com/term-llm/term-llm/internal/modelsdev"
)

const haikuCatalogFixture = `{"anthropic":{"models":{"claude-haiku-5-5":{"cost":{"input":0.1,"output":0.5,"cache_read":0.01,"cache_write":0.125,"tiers":[{"input":0.5,"output":2.5,"cache_read":0.05,"cache_write":0.625,"tier":{"type":"context","size":100000}}]}}}}}`

func proxyCatalog(t *testing.T, body string) *modelsdev.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer server.Close()
	client := modelsdev.NewClient(t.TempDir(), server.Client())
	client.Endpoint = server.URL
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The upstream is gone before any request is priced: only the snapshot is used.
	return client
}
func haikuObserved() ObservedUsage {
	return ObservedUsage{Protocol: usagetap.Messages, Usage: usagetap.Usage{Model: "claude-haiku-5-5", Tier: "standard", Source: usagetap.Reported, Turns: 1, Input: 2, Output: 19, CachedInput: 0, CacheWrite: 36098}}
}
func quotedMoney(m Money) *Money {
	if m.Billing != "plan" {
		return &m
	}
	if m.APIEquivalent == nil {
		return nil
	}
	e := m.APIEquivalent
	return &Money{Kind: e.Kind, Amount: e.Amount, Currency: e.Currency, Source: e.Source, AsOf: e.AsOf, Rate: e.Rate, Reason: e.Reason}
}
func TestMoneyModelsDevHaikuPlanAndAPI(t *testing.T) {
	for _, billing := range []string{"plan", "pay_as_you_go"} {
		t.Run(billing, func(t *testing.T) {
			s := testService(t)
			s.pricing = proxyCatalog(t, haikuCatalogFixture)
			route := Route{Name: "claude", Upstream: "https://api.anthropic.com", Billing: billing}
			observed := haikuObserved()
			rec := Record{Outcome: "complete", Usage: &observed}
			s.finalizeMoney(&rec, route)
			q := quotedMoney(*rec.Money)
			if q == nil || q.Kind != "estimated" || q.Amount == nil || *q.Amount != 4522 || q.Source != "models.dev/anthropic" || q.AsOf == "" || q.Rate == nil {
				t.Fatalf("Haiku catalog quote: %+v", q)
			}
			if !strings.Contains(q.Reason, "catalog default rate") {
				t.Fatal("cache lifetime assumption missing", q.Reason)
			}
			if billing == "plan" && (rec.Money.Amount != nil || rec.Money.Kind != "included") {
				t.Fatal("hypothetical price became spending", rec.Money)
			}
		})
	}
}
func TestMoneyModelsDevContextTierBoundary(t *testing.T) {
	s := testService(t)
	s.pricing = proxyCatalog(t, haikuCatalogFixture)
	route := Route{Name: "claude", Upstream: "https://api.anthropic.com", Billing: "plan"}
	for _, test := range []struct{ input, want int64 }{{100000, 10000}, {100001, 50001}} {
		o := haikuObserved()
		o.Usage.Input = test.input
		o.Usage.Output = 0
		o.Usage.CacheWrite = 0
		m := finishRequestMoneyUsingCatalog(offlineMoneyInput(route, o), route, &o, nil, s.catalogMoney(route))
		q := quotedMoney(m)
		if q == nil || q.Amount == nil || *q.Amount != test.want {
			t.Fatalf("input %d: %+v", test.input, q)
		}
	}
	o := haikuObserved()
	o.Usage.Turns = 2
	m := finishRequestMoneyUsingCatalog(offlineMoneyInput(route, o), route, &o, nil, s.catalogMoney(route))
	if m.APIEquivalent.Amount != nil {
		t.Fatal("aggregated context priced as one request", m)
	}
}
func TestMoneyModelsDevPrecedenceAndIncompleteRates(t *testing.T) {
	s := testService(t)
	s.pricing = proxyCatalog(t, haikuCatalogFixture)
	route := Route{Name: "claude", Upstream: "https://api.anthropic.com", Billing: "plan"}
	o := haikuObserved()
	in := offlineMoneyInput(route, o)
	card := MoneyRate{Provider: route.Name, Model: in.Model, Tier: in.Tier, Currency: "EUR", Source: "operator", AsOf: "2026-11-16", Input: moneyPtr(1000000), Output: moneyPtr(2000000), CacheRead: moneyPtr(0), CacheWrite: moneyPtr(3000000)}
	m := finishRequestMoneyUsingCatalog(in, route, &o, []MoneyRate{card}, s.catalogMoney(route))
	if m.APIEquivalent.Source != "operator" || m.APIEquivalent.Currency != "EUR" {
		t.Fatal("catalog displaced override", m.APIEquivalent)
	}
	card.CacheWrite = nil
	m = finishRequestMoneyUsingCatalog(in, route, &o, []MoneyRate{card}, s.catalogMoney(route))
	if m.APIEquivalent.Amount != nil {
		t.Fatal("catalog filled explicit card hole", m.APIEquivalent)
	}
	// Even for a bundled model, an incomplete newer catalog entry is authoritative.
	s.pricing = proxyCatalog(t, `{"anthropic":{"models":{"claude-opus-5":{"cost":{"input":5,"output":25,"cache_read":0.5}}}}}`)
	o.Usage.Model = "claude-opus-5"
	m = finishRequestMoneyUsingCatalog(offlineMoneyInput(route, o), route, &o, nil, s.catalogMoney(route))
	if m.APIEquivalent.Amount != nil {
		t.Fatal("bundled write price filled missing catalog rate", m.APIEquivalent)
	}
}
func TestMoneyModelsDevUnknownEvidence(t *testing.T) {
	s := testService(t)
	s.pricing = proxyCatalog(t, haikuCatalogFixture)
	route := Route{Name: "claude", Upstream: "https://api.anthropic.com", Billing: "plan"}
	for _, test := range []struct {
		name   string
		change func(*ObservedUsage)
	}{
		{"unknown cache", func(o *ObservedUsage) { o.Usage.CachedInput = -1 }},
		{"unknown writes", func(o *ObservedUsage) { o.Usage.CacheWrite = -1 }},
		{"ambiguous", func(o *ObservedUsage) { o.Usage.Ambiguous = true }},
		{"priority", func(o *ObservedUsage) { o.Usage.Tier = "priority" }},
		{"unknown model", func(o *ObservedUsage) { o.Usage.Model = "haiku" }},
		{"wrong protocol", func(o *ObservedUsage) { o.Protocol = usagetap.Ollama }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := haikuObserved()
			test.change(&o)
			m := finishRequestMoneyUsingCatalog(offlineMoneyInput(route, o), route, &o, nil, s.catalogMoney(route))
			if m.APIEquivalent == nil || m.APIEquivalent.Amount != nil {
				t.Fatal("unknown evidence became price", m.APIEquivalent)
			}
		})
	}
}
func TestMoneyModelsDevProviderIdentity(t *testing.T) {
	catalog := proxyCatalog(t, `{"independent":{"api":"https://independent.example/v1","models":{"fixture":{}}},"anthropic":{"models":{}}}`)
	for _, test := range []struct{ upstream, account, want string }{
		{"https://api.anthropic.com", "", "anthropic"},
		{"https://api.anthropic.com/v1/", "", "anthropic"},
		{"https://api.venice.ai/api/v1", "", "venice"},
		{"https://generativelanguage.googleapis.com/v1beta", "", "google"},
		{"https://chatgpt.com/backend-api/codex", "family", "openai"},
		{"https://chatgpt.com/backend-api/codex", "", ""},
		{"https://independent.example/v1", "", "independent"},
		{"https://api.anthropic.com.attacker.example", "", ""},
		{"https://api.anthropic.com?key=x", "", ""},
		{"http://api.anthropic.com", "", ""},
	} {
		got := catalogProvider(Route{Name: "anthropic", Upstream: test.upstream, Account: test.account}, catalog)
		if got != test.want {
			t.Errorf("%s: %q, want %q", test.upstream, got, test.want)
		}
	}
}

func TestMoneyModelsDevRefreshCancellationAndOffline(t *testing.T) {
	t.Setenv("TERM_LLM_OFFLINE", "")
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	s := testService(t)
	s.pricing = modelsdev.NewClient("", server.Client())
	s.pricing.Endpoint = server.URL
	stop := s.startPricingRefresh(context.Background())
	<-started
	stop()
	<-canceled
	t.Setenv("TERM_LLM_OFFLINE", "1")
	// A canceled parent is not needed in offline mode; the worker never starts.
	stop = s.startPricingRefresh(context.Background())
	stop()
}
