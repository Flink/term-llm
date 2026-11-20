package responsesproviders

import (
	"context"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const maxProxyChatGPTCatalogs = 32

// requestModelEffort resolves the configured default and request overrides using
// the same delegated catalog that ListModels uses. Fetch lazily so ordinary
// model IDs don't need a catalog round trip before the first request.
func (p *ChatGPTProvider) requestModelEffort(ctx context.Context, model, explicitEffort string) (string, string) {
	delegated := p.baseURL != "" && p.creds.Delegated()
	catalog := sync.OnceValue(func() []ModelInfo {
		models, _ := p.proxyChatGPTModelFacts(ctx, false)
		return models // On failure, retain the existing local parser's fallback.
	})
	parse := func(model string) (string, string) {
		if candidate, ok := trimKnownEffortSuffix(model); delegated && ok {
			models := catalog()
			for _, info := range models {
				if info.ID == model {
					return model, "" // Exact IDs win over suffixes.
				}
			}
			for _, info := range models {
				if info.ID == candidate && len(info.ReasoningEfforts) > 0 {
					effort := model[len(candidate)+1:]
					if slices.Contains(info.ReasoningEfforts, effort) {
						return candidate, effort
					}
					return model, ""
				}
			}
		}
		return parseModelEffortForProvider("chatgpt", model)
	}
	base, effort := parse(model)
	if explicitEffort != "" {
		return base, explicitEffort
	}
	if effort == "" {
		effort = p.effort
		if delegated && model != p.model {
			_, effort = parse(p.model)
		}
	}
	return base, effort
}

// Account catalogs can contain private model availability. Delegated credentials
// have a synthetic ID derived from the proxy root and scoped client token. Keep
// those catalogs in memory only, isolated from direct OAuth and other routes.
var proxyChatGPTCatalogs = struct {
	sync.Mutex
	entries map[string]chatGPTModelsCache
	refresh singleflight.Group
}{entries: make(map[string]chatGPTModelsCache)}

func (p *ChatGPTProvider) proxyChatGPTModelFacts(ctx context.Context, force bool) ([]ModelInfo, error) {
	key := p.baseURL + "\x00" + p.creds.AccountID
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !force {
		if models := freshProxyChatGPTCatalog(key); models != nil {
			return cloneChatGPTCatalog(models), nil
		}
	}
	ch := proxyChatGPTCatalogs.refresh.DoChan(key, func() (any, error) {
		if !force {
			if models := freshProxyChatGPTCatalog(key); models != nil {
				return models, nil
			}
		}
		// A shared fetch has a strict deadline but one canceled waiter does not
		// cancel the catalog lookup for everyone else.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatGPTModelsTimeout)
		defer cancel()
		models, err := p.refreshChatGPTModelFacts(fetchCtx)
		if err != nil {
			return nil, err
		}
		rememberProxyChatGPTCatalog(key, models)
		return models, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return cloneChatGPTCatalog(result.Val.([]ModelInfo)), nil
	}
}

func freshProxyChatGPTCatalog(key string) []ModelInfo {
	proxyChatGPTCatalogs.Lock()
	defer proxyChatGPTCatalogs.Unlock()
	entry := proxyChatGPTCatalogs.entries[key]
	if time.Since(entry.FetchedAt) > chatGPTModelsCacheTTL {
		return nil
	}
	return entry.Models
}

func rememberProxyChatGPTCatalog(key string, models []ModelInfo) {
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
	entries[key] = chatGPTModelsCache{FetchedAt: time.Now(), Models: models}
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
