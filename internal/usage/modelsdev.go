package usage

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/modelsdev"
)

// modelsDevURL is the OpenCode model directory: a community-maintained catalog
// of every model's rates, refreshed far more often than a release of this
// program. It is the source of truth when it is available; the bundled table
// stays as the offline answer.
var modelsDevURL = "https://models.dev/api.json"

// modelsDevRate projects optional catalog rates onto usage's established numeric
// pricing representation. Usage keeps its bundled fallback rules; exact consumers
// should use modelsdev.Client.Lookup to retain missing versus explicit zero.
func modelsDevRate(rate *float64) float64 {
	if rate == nil {
		return 0
	}
	return *rate
}

func modelsDevModelPricing(c modelsdev.Cost) (ModelPricing, bool) {
	const perMillion = 1_000_000
	input, output := modelsDevRate(c.Input), modelsDevRate(c.Output)
	read, write := modelsDevRate(c.CacheRead), modelsDevRate(c.CacheWrite)
	// Preserve usage's behavior: completely free entries do not replace the
	// bundled card with an apparent zero-cost model.
	if input == 0 && output == 0 && read == 0 && write == 0 {
		return ModelPricing{}, false
	}
	pricing := ModelPricing{
		InputCostPerToken:           input / perMillion,
		OutputCostPerToken:          output / perMillion,
		CacheReadInputTokenCost:     read / perMillion,
		CacheCreationInputTokenCost: write / perMillion,
	}
	for _, tier := range c.Tiers {
		if tier.Tier.Type != "context" || tier.Tier.Size <= 0 {
			continue
		}
		pricing.InputCostPerTokenAbove200k = modelsDevRate(tier.Input) / perMillion
		pricing.OutputCostPerTokenAbove200k = modelsDevRate(tier.Output) / perMillion
		pricing.CacheReadCostAbove200k = modelsDevRate(tier.CacheRead) / perMillion
		pricing.CacheCreationCostAbove200k = modelsDevRate(tier.CacheWrite) / perMillion
		pricing.TieredThreshold = tier.Tier.Size
		pricing.WholeRequestTier = true
		break
	}
	return pricing, true
}

// modelsDevPricing is the derived catalog: model name to rates, keyed both bare
// and provider-qualified. The shared catalog is cached on disk; this small
// projection stays in memory so usage rendering does not repeatedly parse it.
type modelsDevPricing struct {
	mu sync.RWMutex
	// dir records which cache the entries came from. Two fetchers may point at
	// different directories — tests always do — and one must never answer with
	// the other's catalog.
	dir     string
	loaded  bool
	entries map[string]ModelPricing
}

var modelsDevStore = &modelsDevPricing{}

// lookupModelsDevPricing resolves a model against the cached catalog. It never
// fetches: cost rendering runs on the display path and must not block on the
// network.
func lookupModelsDevPricing(cacheDir, modelName string) (ModelPricing, bool) {
	if !modelsDevStore.ensureLoaded(cacheDir) {
		return ModelPricing{}, false
	}
	modelsDevStore.mu.RLock()
	defer modelsDevStore.mu.RUnlock()
	for _, candidate := range pricingLookupCandidates(modelName) {
		if pricing, ok := modelsDevStore.entries[candidate]; ok {
			return pricing, true
		}
	}
	return ModelPricing{}, false
}

func (s *modelsDevPricing) ensureLoaded(cacheDir string) bool {
	s.mu.RLock()
	// Evaluate under the read lock: replace() writes entries under the write
	// lock, so reading the map header outside it is a race.
	if s.loaded && s.dir == cacheDir {
		available := len(s.entries) > 0
		s.mu.RUnlock()
		return available
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.dir == cacheDir {
		return len(s.entries) > 0
	}
	s.loaded, s.dir, s.entries = true, cacheDir, nil
	client := modelsdev.NewClient(cacheDir, nil)
	if err := client.Load(); err != nil {
		return false
	}
	catalog, _ := client.Snapshot()
	s.entries = projectModelsDevPricing(catalog)
	return len(s.entries) > 0
}

func (s *modelsDevPricing) replace(cacheDir string, entries map[string]ModelPricing) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries, s.loaded, s.dir = entries, true, cacheDir
}

// RefreshDirectoryInBackground refreshes the cached model directory without
// blocking the caller. Prices are only ever read from the cache, so a program
// that starts, runs and exits before the fetch lands simply uses the previous
// catalog or the bundled table.
func RefreshDirectoryInBackground(ctx context.Context) {
	// A program told to stay offline prices from the bundled table only. Tests
	// set this too: a background fetch that lands mid-run would swap the rates
	// their assertions are written against.
	if strings.TrimSpace(os.Getenv("TERM_LLM_OFFLINE")) != "" {
		return
	}
	go func() {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		_ = NewPricingFetcher().RefreshModelsDevPricing(refreshCtx)
	}()
}

// RefreshModelsDevPricing updates the cached model directory when the copy on
// disk is older than the refresh interval. It is safe to call on a background
// goroutine at startup; callers that want the bundled table only can skip it.
func (p *PricingFetcher) RefreshModelsDevPricing(ctx context.Context) error {
	if p == nil || p.httpClient == nil {
		return nil
	}
	client := modelsdev.NewClient(p.cacheDir, p.httpClient)
	client.Endpoint = modelsDevEndpoint()
	err := client.Refresh(ctx)
	catalog, _ := client.Snapshot()
	entries := projectModelsDevPricing(catalog)
	if len(entries) > 0 {
		modelsDevStore.replace(p.cacheDir, entries)
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("models.dev catalog carried no priced models")
	}
	return nil
}

// Keep the historical URL test hook without another HTTP or schema path.
func modelsDevEndpoint() string {
	if os.Getenv("MODELS_DEV_BASE_URL") != "" {
		return modelsdev.DefaultURL
	}
	return modelsDevURL
}

func fetchModelsDevPricing(ctx context.Context, client *http.Client) (map[string]ModelPricing, error) {
	catalog, err := modelsdev.Fetch(ctx, client, modelsDevEndpoint())
	if err != nil {
		return nil, err
	}
	return projectModelsDevPricing(catalog), nil
}

// firstPartyProviders claim a bare model name ahead of the dozens of resellers
// that relist the same models. A reseller's rate is a real price, but it is not
// the price of "opus" — and resellers frequently publish only input and output,
// which would silently drop cache rates that dominate an agent's bill.
var firstPartyProviders = []string{
	"anthropic", "openai", "google", "google-vertex", "xai", "deepseek", "zai", "z-ai",
	"moonshotai", "minimax", "mistral", "meta", "amazon-bedrock", "azure", "cohere",
	"opencode",
}

func providerRank(provider string) int {
	for rank, known := range firstPartyProviders {
		if strings.EqualFold(provider, known) {
			return rank
		}
	}
	return len(firstPartyProviders)
}

// projectModelsDevPricing flattens the catalog. A bare model name is claimed by
// the highest-ranked provider that lists it; the qualified "provider/model" key
// always resolves exactly.
func projectModelsDevPricing(catalog modelsdev.Catalog) map[string]ModelPricing {
	entries := make(map[string]ModelPricing, 512)
	providers := make([]string, 0, len(catalog))
	for provider := range catalog {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	sortByRank(providers)
	for _, provider := range providers {
		for model, entry := range catalog[provider].Models {
			if entry.Cost == nil {
				continue
			}
			pricing, ok := modelsDevModelPricing(*entry.Cost)
			if !ok {
				continue
			}
			entries[provider+"/"+model] = pricing
			if _, exists := entries[model]; !exists {
				entries[model] = pricing
			}
		}
	}
	return entries
}

// sortByRank is stable, so providers of equal rank keep their alphabetical
// order and the projection is deterministic.
func sortByRank(providers []string) {
	sort.SliceStable(providers, func(i, j int) bool {
		return providerRank(providers[i]) < providerRank(providers[j])
	})
}
