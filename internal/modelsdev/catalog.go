package modelsdev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/term-llm/term-llm/internal/providerhttp"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const (
	DefaultURL = "https://models.dev/api.json"
	// MaxBodyBytes bounds both downloaded catalogs and disk snapshots.
	MaxBodyBytes int64 = 32 << 20
)

// Catalog retains exact provider namespaces and model keys. Lookup policy (such
// as aliases or preferring first-party prices) belongs to individual consumers.
type Catalog map[string]Provider

type Provider struct {
	ID     string           `json:"id,omitempty"`
	Name   string           `json:"name,omitempty"`
	API    string           `json:"api,omitempty"`
	Models map[string]Model `json:"models"`
}

type Model struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Cost *Cost  `json:"cost,omitempty"`
}

// Cost is in USD per million tokens. Nil means unpublished, not free. In
// particular CacheWrite is only the published baseline: no cache lifetime or
// one-hour multiplier is inferred by this package.
type Cost struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Tiers      []Tier   `json:"tiers,omitempty"`
}

type Tier struct {
	Input      *float64     `json:"input,omitempty"`
	Output     *float64     `json:"output,omitempty"`
	CacheRead  *float64     `json:"cache_read,omitempty"`
	CacheWrite *float64     `json:"cache_write,omitempty"`
	Tier       TierBoundary `json:"tier"`
}

type TierBoundary struct {
	Size int    `json:"size"`
	Type string `json:"type"`
}

// Decode reads a single bounded catalog, rejecting malformed/trailing JSON.
func Decode(r io.Reader) (Catalog, error) {
	body, err := providerhttp.ReadBounded(r, MaxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read models.dev catalog: %w", err)
	}
	var catalog Catalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	if err := validate(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validate(catalog Catalog) error {
	if len(catalog) == 0 {
		return fmt.Errorf("models.dev catalog carried no providers")
	}
	for provider, entry := range catalog {
		if provider == "" || entry.Models == nil {
			return fmt.Errorf("models.dev catalog has invalid provider %q", provider)
		}
		for model, entry := range entry.Models {
			if model == "" {
				return fmt.Errorf("models.dev catalog has empty model key in %q", provider)
			}
			if entry.Cost == nil {
				continue
			}
			cost := entry.Cost
			if !validRates(cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite) {
				return fmt.Errorf("models.dev catalog has invalid rates for %s/%s", provider, model)
			}
			for _, tier := range cost.Tiers {
				if !validRates(tier.Input, tier.Output, tier.CacheRead, tier.CacheWrite) {
					return fmt.Errorf("models.dev catalog has invalid tier rates for %s/%s", provider, model)
				}
			}
		}
	}
	return nil
}

func validRates(rates ...*float64) bool {
	for _, rate := range rates {
		if rate != nil && *rate < 0 {
			return false
		}
	}
	return true
}

// Fetch uses the application's delegated proxy routing while retaining the
// injected client's transport, timeout and TLS settings. Empty/default endpoint
// honors MODELS_DEV_BASE_URL; a nondefault endpoint is an explicit override.
func Fetch(ctx context.Context, client *http.Client, endpoint string) (Catalog, error) {
	if endpoint == "" || endpoint == DefaultURL {
		base, err := providerhttp.EnvBaseURL("MODELS_DEV_BASE_URL", "https://models.dev")
		if err != nil {
			return nil, err
		}
		endpoint = base + "/api.json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create models.dev request: %w", err)
	}
	req.Header.Set("User-Agent", "term-llm")
	resp, err := proxyclient.Do(client, req)
	if err != nil {
		return nil, fmt.Errorf("fetch models.dev catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev catalog: unexpected status %d", resp.StatusCode)
	}
	return Decode(resp.Body)
}

func cloneCatalog(catalog Catalog) Catalog {
	if catalog == nil {
		return nil
	}
	copy := make(Catalog, len(catalog))
	for key, provider := range catalog {
		models := make(map[string]Model, len(provider.Models))
		for key, model := range provider.Models {
			models[key] = cloneModel(model)
		}
		provider.Models = models
		copy[key] = provider
	}
	return copy
}

func cloneModel(model Model) Model {
	if model.Cost != nil {
		cost := *model.Cost
		cost.Input, cost.Output = cloneRate(cost.Input), cloneRate(cost.Output)
		cost.CacheRead, cost.CacheWrite = cloneRate(cost.CacheRead), cloneRate(cost.CacheWrite)
		cost.Tiers = append([]Tier(nil), cost.Tiers...)
		for i := range cost.Tiers {
			tier := &cost.Tiers[i]
			tier.Input, tier.Output = cloneRate(tier.Input), cloneRate(tier.Output)
			tier.CacheRead, tier.CacheWrite = cloneRate(tier.CacheRead), cloneRate(tier.CacheWrite)
		}
		model.Cost = &cost
	}
	return model
}

func cloneRate(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	copy := *rate
	return &copy
}
