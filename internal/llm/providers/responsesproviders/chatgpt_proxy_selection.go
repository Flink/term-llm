package responsesproviders

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// ChatGPTCatalogModelServiceTier peels off -fast only when the supplied catalog
// advertises priority for the exact base ID or the supported effort variant.
// A literal catalog ID takes precedence over any suffix interpretation.
func ChatGPTCatalogModelServiceTier(catalog []ModelInfo, model string) (string, string) {
	model = strings.TrimSpace(model)
	if !strings.HasSuffix(model, "-fast") || slices.ContainsFunc(catalog, func(info ModelInfo) bool { return info.ID == model }) {
		return model, ""
	}
	candidate := strings.TrimSuffix(model, "-fast")
	base, _, _, _ := SplitCatalogModelEffort(catalog, candidate)
	for _, info := range catalog {
		if info.ID == base && ModelSupportsFast(info) {
			return candidate, ServiceTierFast
		}
	}
	return model, ""
}

// requestModelSelection resolves model, effort and service tier together. A
// delegated request never consults the direct account's catalog to parse a suffix.
func (p *ChatGPTProvider) requestModelSelection(ctx context.Context, model, explicitEffort string) (string, string, string) {
	if p.baseURL == "" || !p.creds.Delegated() {
		model, tier := chatGPTModelServiceTier(model)
		base, effort := parseModelEffortForProvider("chatgpt", model)
		return base, firstNonEmpty(explicitEffort, effort, p.effort), firstNonEmpty(tier, p.serviceTier)
	}

	catalog := sync.OnceValue(func() []ModelInfo {
		models, err := p.proxyChatGPTModelFacts(ctx, false)
		if err == nil {
			return models
		}
		// A transient refresh failure must not change what a previously
		// advertised selection means. Never fall back to another route.
		return p.cachedDelegatedModelFacts()
	})
	base, effort, tier := delegatedChatGPTSelection(catalog, model)
	if model != p.model && (tier == "" || (effort == "" && explicitEffort == "")) {
		_, defaultEffort, defaultTier := delegatedChatGPTSelection(catalog, p.model)
		effort = firstNonEmpty(effort, defaultEffort)
		tier = firstNonEmpty(tier, defaultTier)
	}
	return base, firstNonEmpty(explicitEffort, effort), firstNonEmpty(tier, p.serviceTier)
}

func delegatedChatGPTSelection(catalog func() []ModelInfo, model string) (base, effort, tier string) {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(model, "-fast") {
		models := catalog()
		candidate, tier := ChatGPTCatalogModelServiceTier(models, model)
		if tier == "" {
			return model, "", ""
		}
		base, effort, _, _ := SplitCatalogModelEffort(models, candidate)
		return base, effort, tier
	}
	candidate, hasSuffix := trimKnownEffortSuffix(model)
	if !hasSuffix {
		return model, "", ""
	}
	base, effort, _, found := SplitCatalogModelEffort(catalog(), model)
	if !found {
		// Known bundled models still work when not listed by the proxy. Do
		// not let unscoped metadata override a literal ID or advertised efforts.
		if info, ok := staticChatGPTModelInfo(candidate); ok && slices.Contains(info.ReasoningEfforts, model[len(candidate)+1:]) {
			return candidate, model[len(candidate)+1:], ""
		}
	}
	return base, effort, ""
}

func (p *ChatGPTProvider) cachedDelegatedModelFacts() []ModelInfo {
	scope, err := p.delegatedCatalogScope()
	if err != nil {
		return nil
	}
	if models := freshProxyChatGPTCatalog(scope.id); models != nil {
		return models
	}
	entry, _ := scope.load()
	return entry.Models
}
