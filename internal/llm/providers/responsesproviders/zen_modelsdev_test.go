package responsesproviders

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Optional cost fields must not turn unpublished prices into free prices. Zen
// still uses its own model-list/cache lifecycle and free-route filtering.
func TestZenListModelsSharedCatalogOptionalRates(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	modelsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"paid"},{"id":"partial"},{"id":"unpriced"},{"id":"big-pickle"},{"id":"paid-free"}]}`))
	}))
	t.Cleanup(modelsServer.Close)
	pricingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "term-llm" {
			t.Errorf("catalog user agent = %q", r.UserAgent())
		}
		_, _ = w.Write([]byte(`{"opencode":{"models":{
		 "paid":{"name":"Paid Model","cost":{"input":1.25,"output":5}},
		 "partial":{"name":"Partial Model","cost":{"input":0}},
		 "unpriced":{"name":"Unpriced Model"},
		 "big-pickle":{"cost":{"input":0,"output":0}},
		 "paid-free":{"cost":{"input":0,"output":0}}
		}},"another-provider":{"models":{"paid":{"name":"Wrong Provider","cost":{"input":99,"output":99}}}}}`))
	}))
	t.Cleanup(pricingServer.Close)
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"opencode":{"models":{"paid":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}`))
	}))
	t.Cleanup(catalogServer.Close)
	previousPricing, previousCatalog := modelsDevURL, zenCatalogURL
	modelsDevURL, zenCatalogURL = pricingServer.URL, catalogServer.URL
	t.Cleanup(func() { modelsDevURL, zenCatalogURL = previousPricing, previousCatalog })

	provider := &ZenProvider{OpenAICompatProvider: NewOpenAICompatProvider(modelsServer.URL, "", "", zenDisplayName)}
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v", models)
	}
	byID := make(map[string]ModelInfo)
	for _, model := range models {
		byID[model.ID] = model
	}
	paid := byID["paid"]
	if paid.DisplayName != "Paid Model" || paid.InputPrice != 1.25 || paid.OutputPrice != 5 || !equalSlice(paid.ReasoningEfforts, []string{"low", "high"}) {
		t.Fatalf("paid model enrichment = %+v", paid)
	}
	partial := byID["partial"]
	if partial.DisplayName != "Partial Model" || partial.InputPrice != 0 || partial.OutputPrice != -1 {
		t.Fatalf("partial model missing/zero prices = %+v", partial)
	}
	if unknown := byID["unpriced"]; unknown.InputPrice != -1 || unknown.OutputPrice != -1 || unknown.DisplayName != "Unpriced Model" {
		t.Fatalf("unpriced model = %+v", unknown)
	}
	cached := GetCachedZenModelInfos()
	if len(cached) != 3 {
		t.Fatalf("cached model metadata = %+v", cached)
	}
}
