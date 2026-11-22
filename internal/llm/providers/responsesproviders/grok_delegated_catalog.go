package responsesproviders

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

var errGrokCatalogBinding = errors.New("Grok catalog requires an exact registered Grok proxy binding")

func delegatedGrokCatalog(baseURL string) (grokCatalogScope, proxyclient.Binding, error) {
	binding, ok := delegatedCatalogBinding(baseURL, "grok")
	if !ok {
		return grokCatalogScope{}, proxyclient.Binding{}, errGrokCatalogBinding
	}
	endpoint := binding.Root + "/models"
	scopeID := delegatedCatalogID(binding, endpoint)
	return grokCatalogScope{file: "grok_delegated_models_" + scopeID + ".json", accountID: scopeID, origin: endpoint}, binding, nil
}

// CachedDelegatedGrokModels reads the registered route's last known catalog
// without network access. A missing cache is not an error.
func CachedDelegatedGrokModels(baseURL string) ([]ModelInfo, bool, error) {
	scope, _, err := delegatedGrokCatalog(baseURL)
	if err != nil {
		return nil, false, err
	}
	cache, err := scope.load()
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return cache.Models, err == nil && time.Since(cache.FetchedAt) <= grokModelsCacheTTL, err
}

// RefreshDelegatedGrokModelsIfStale updates a missing or stale catalog using
// only the registered Grok proxy binding. A failed fetch leaves the cache alone.
func RefreshDelegatedGrokModelsIfStale(ctx context.Context, baseURL string) error {
	scope, binding, err := delegatedGrokCatalog(baseURL)
	if err != nil {
		return err
	}
	if cache, err := scope.load(); err == nil && time.Since(cache.FetchedAt) <= grokModelsCacheTTL {
		return nil
	}
	models, err := fetchDelegatedGrokModels(ctx, scope.origin, binding.Token)
	if err != nil {
		return err
	}
	return scope.save(grokModelsCache{FetchedAt: time.Now(), Models: models})
}

func listDelegatedGrokModels(ctx context.Context, baseURL string) ([]ModelInfo, error) {
	scope, binding, err := delegatedGrokCatalog(baseURL)
	if err != nil {
		return nil, err
	}
	models, err := fetchDelegatedGrokModels(ctx, scope.origin, binding.Token)
	if err == nil {
		_ = scope.save(grokModelsCache{FetchedAt: time.Now(), Models: models})
	}
	return models, err
}

func fetchDelegatedGrokModels(ctx context.Context, endpoint, token string) ([]ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, grokModelsTimeout)
	defer cancel()
	// The proxy owns vendor OAuth and the account ID; a 401 is not a local
	// refresh signal. Authorize with the same binding used to scope the cache.
	return requestGrokModels(ctx, endpoint, token, "")
}

func splitDelegatedGrokModel(catalog []ModelInfo, model string) (string, string) {
	model = strings.TrimSpace(model)
	if base, effort, _, found := SplitCatalogModelEffort(catalog, model); found {
		return base, effort
	}
	if base, ok := trimKnownEffortSuffix(model); ok {
		effort := model[len(base)+1:]
		if slices.Contains(staticGrokReasoningEfforts(base), effort) {
			return base, effort
		}
	}
	return model, ""
}

// A nil context is used by Name, which must not make network requests.
func (p *GrokProvider) resolveGrokModel(ctx context.Context, requested string) (string, string, []ModelInfo) {
	if !p.creds.Delegated() {
		model, effort := parseModelEffortForProvider("grok", requested)
		return chooseModel(model, p.model), chooseModel(effort, p.effort), nil
	}
	catalog, _, _ := CachedDelegatedGrokModels(p.baseURL)
	_, requestHasSuffix := trimKnownEffortSuffix(strings.TrimSpace(requested))
	_, defaultHasSuffix := trimKnownEffortSuffix(strings.TrimSpace(p.model))
	if ctx != nil && len(catalog) == 0 && (requestHasSuffix || defaultHasSuffix) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		catalog, _ = listDelegatedGrokModels(ctx, p.baseURL)
	}
	model, effort := splitDelegatedGrokModel(catalog, p.model)
	if strings.TrimSpace(requested) != "" {
		if override, overrideEffort := splitDelegatedGrokModel(catalog, requested); override != model || overrideEffort != "" {
			return override, overrideEffort, catalog
		}
	}
	return model, effort, catalog
}

func (p *GrokProvider) clampOutputTokens(requested int, model string, catalog []ModelInfo) int {
	if !p.creds.Delegated() {
		return clampGrokOutputTokens(requested, model)
	}
	for _, info := range catalog {
		if info.ID == model && info.OutputLimit > 0 {
			return min(requested, info.OutputLimit)
		}
	}
	return ClampOutputTokens(requested, model)
}
