package responsesproviders

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/cache"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const copilotCatalogRefreshTimeout = 30 * time.Second

var errCopilotCatalogBinding = errors.New("Copilot catalog requires an exact registered Copilot proxy binding")

func copilotCatalogBinding(baseURL string) (proxyclient.Binding, error) {
	binding, ok := delegatedCatalogBinding(baseURL, "copilot")
	if !ok {
		return proxyclient.Binding{}, errCopilotCatalogBinding
	}
	return binding, nil
}

func copilotDelegatedCatalogKey(binding proxyclient.Binding) string {
	return "copilot-delegated-" + delegatedCatalogID(binding, binding.Root)
}

// CachedDelegatedCopilotModels reads the last catalog for the registered root
// and caller without network access. The bool reports freshness; stale models
// are still returned, and a missing catalog is not an error.
func CachedDelegatedCopilotModels(baseURL string) ([]ModelInfo, bool, error) {
	binding, err := copilotCatalogBinding(baseURL)
	if err != nil {
		return nil, false, err
	}
	cached, err := cache.ReadModelCache(copilotDelegatedCatalogKey(binding))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return modelInfosFromCache(cached), cache.IsCacheValid(cached), nil
}

// RefreshDelegatedCopilotModelsIfStale fetches a missing or stale catalog
// using only the registered proxy binding, without local OAuth or fallback.
// A failed fetch leaves the previous catalog intact.
func RefreshDelegatedCopilotModelsIfStale(ctx context.Context, baseURL string) error {
	binding, err := copilotCatalogBinding(baseURL)
	if err != nil {
		return err
	}
	key := copilotDelegatedCatalogKey(binding)
	if cached, err := cache.ReadModelCache(key); err == nil && cache.IsCacheValid(cached) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, copilotCatalogRefreshTimeout)
	defer cancel()
	models, err := fetchCopilotModels(ctx, binding.Root+"/models", binding.Token)
	if err != nil {
		return err
	}
	return cache.WriteModelInfoCache(key, modelInfosToCache(models))
}

func listDelegatedCopilotModels(ctx context.Context, baseURL string) ([]ModelInfo, error) {
	binding, err := copilotCatalogBinding(baseURL)
	if err != nil {
		return nil, err
	}
	models, err := fetchCopilotModels(ctx, binding.Root+"/models", binding.Token)
	if err == nil {
		_ = cache.WriteModelInfoCache(copilotDelegatedCatalogKey(binding), modelInfosToCache(models))
	}
	return models, err
}

// DelegatedCopilotInputLimit keeps runtime context limits scoped to the same
// route as model discovery. A cold cache may be filled within the caller's deadline.
func (p *CopilotProvider) DelegatedCopilotInputLimit(ctx context.Context, model string) (int, bool) {
	if !p.creds.Delegated() {
		return 0, false
	}
	if strings.TrimSpace(model) == "" {
		return 0, true
	}
	models, fresh, _ := CachedDelegatedCopilotModels(p.overrideBaseURL)
	if !fresh && RefreshDelegatedCopilotModelsIfStale(ctx, p.overrideBaseURL) == nil {
		models, _, _ = CachedDelegatedCopilotModels(p.overrideBaseURL)
	}
	return copilotCatalogInputLimit(models, model), true
}
