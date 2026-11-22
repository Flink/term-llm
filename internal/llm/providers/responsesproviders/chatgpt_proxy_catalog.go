package responsesproviders

import (
	"context"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const maxProxyChatGPTCatalogs = 32

var proxyChatGPTCatalogs = struct {
	sync.Mutex
	entries map[string]chatGPTModelsCache
	refresh singleflight.Group
}{entries: make(map[string]chatGPTModelsCache)}

type proxyChatGPTCatalogResult struct {
	models  []ModelInfo
	fetched bool
}

func (p *ChatGPTProvider) proxyChatGPTModelFacts(ctx context.Context, force bool) ([]ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope, err := p.delegatedCatalogScope()
	if err != nil {
		return nil, err
	}
	key := scope.id
	if !force {
		if models := freshProxyChatGPTCatalog(key); models != nil {
			return cloneChatGPTCatalog(models), nil
		}
	}
	refresh := func() (any, error) {
		if !force {
			if models := freshProxyChatGPTCatalog(key); models != nil {
				return proxyChatGPTCatalogResult{models: models}, nil
			}
			if entry, err := scope.load(); err == nil && entry.fresh() {
				rememberProxyChatGPTCatalog(key, entry)
				return proxyChatGPTCatalogResult{models: entry.Models}, nil
			}
		}
		// A shared fetch has a strict deadline but one canceled waiter does not
		// cancel the catalog lookup for everyone else.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatGPTModelsTimeout)
		defer cancel()
		models, err := scope.fetch(fetchCtx, p.creds)
		if err != nil {
			return nil, err
		}
		entry := scope.entry(models)
		_ = scope.save(entry) // Discovery remains usable when the disk cache cannot be written.
		rememberProxyChatGPTCatalog(key, entry)
		return proxyChatGPTCatalogResult{models: models, fetched: true}, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch := proxyChatGPTCatalogs.refresh.DoChan(key, refresh)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-ch:
			if result.Err != nil {
				return nil, result.Err
			}
			catalog := result.Val.(proxyChatGPTCatalogResult)
			// A force caller can join a normal lookup that just read disk.
			// Only an actual network fetch satisfies forced refresh.
			if !force || catalog.fetched {
				return cloneChatGPTCatalog(catalog.models), nil
			}
		}
	}
}

func freshProxyChatGPTCatalog(key string) []ModelInfo {
	proxyChatGPTCatalogs.Lock()
	defer proxyChatGPTCatalogs.Unlock()
	entry := proxyChatGPTCatalogs.entries[key]
	if !entry.fresh() {
		return nil
	}
	return entry.Models
}

func rememberProxyChatGPTCatalog(key string, catalog chatGPTModelsCache) {
	proxyChatGPTCatalogs.Lock()
	defer proxyChatGPTCatalogs.Unlock()
	entries := proxyChatGPTCatalogs.entries
	if _, exists := entries[key]; !exists && len(entries) >= maxProxyChatGPTCatalogs {
		var oldest string
		var earliest time.Time
		for candidate, entry := range entries {
			if oldest == "" || entry.FetchedAt.Before(earliest) {
				oldest, earliest = candidate, entry.FetchedAt
			}
		}
		delete(entries, oldest)
	}
	entries[key] = catalog
}

func cloneChatGPTCatalog(models []ModelInfo) []ModelInfo {
	cloned := slices.Clone(models)
	for i := range cloned {
		m := &cloned[i]
		m.ServiceTiers = slices.Clone(m.ServiceTiers)
		m.AdditionalSpeedTiers = slices.Clone(m.AdditionalSpeedTiers)
		m.ReasoningEfforts = slices.Clone(m.ReasoningEfforts)
		m.ReasoningModes = slices.Clone(m.ReasoningModes)
	}
	return cloned
}
