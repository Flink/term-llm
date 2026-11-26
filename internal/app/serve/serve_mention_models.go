package serve

import (
	"context"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/llm"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mentions"
)

func (s *serveServer) populateModelMentions(response *serveMentionSearchResponse, text string, cursor int, active mentions.ActiveToken) {
	if active.Quoted || active.Agent || len(active.Query) < 2 {
		return
	}
	var providers []mentions.ModelProvider
	var live []bool
	if s.cfgRef != nil {
		for _, provider := range s.offeredProviders() {
			providers = append(providers, mentions.ModelProvider{ID: provider.Name})
			live = append(live, provider.SupportsListModels)
		}
	}
	token, ok := mentions.ActiveModelToken(text, cursor, providers)
	if !ok {
		return
	}
	for i := range providers {
		provider := &providers[i]
		if token.EffortStep && !mentionEffortProvider(token.Reference, provider.ID) {
			continue
		}
		var pending bool
		provider.Models, pending = s.mentionModelOptions(provider.ID, live[i], time.Now())
		response.ModelsPending = response.ModelsPending || pending
	}
	response.Active = true
	response.ModelOnly = token.Owns()
	response.ModelToken = mentionWireToken(text, token.ActiveToken)
	for _, row := range mentions.CompleteModels(token, providers) {
		response.ModelItems = append(response.ModelItems, serveMentionModelItem(row))
	}
}

func mentionEffortProvider(reference, provider string) bool {
	id, _, ok := strings.Cut(reference, ":")
	return ok && strings.EqualFold(id, provider)
}

func (s *serveServer) mentionModelOptions(provider string, live bool, now time.Time) ([]mentions.ModelOption, bool) {
	// The picker endpoint's fresh upstream cache can answer without scheduling
	// another refresh. Never wait for a constructor holding modelsMu.
	if models := s.tryCachedMentionModels(provider, now); len(models) != 0 {
		return llm.MentionModelOptions(s.cfgRef, provider, models), false
	}
	if live {
		options, pending := s.mentionModels.Snapshot(provider, now, s.refreshMentionModels)
		if options != nil {
			return options, pending
		}
		return llm.LocalMentionModelOptions(s.cfgRef, provider), pending
	}
	return llm.LocalMentionModelOptions(s.cfgRef, provider), false
}

func (s *serveServer) tryCachedMentionModels(provider string, now time.Time) []core.ModelInfo {
	if !s.modelsMu.TryLock() {
		return nil
	}
	defer s.modelsMu.Unlock()
	if entry := s.modelsCache[provider]; now.Before(entry.expiresAt) {
		// The cache replaces whole entries and never mutates their rows.
		return entry.models
	}
	return nil
}

// Discovery outlives an individual search, but never intentionally outlives serve.
func (s *serveServer) refreshMentionModels(ctx context.Context, provider string) []mentions.ModelOption {
	ctx, cancel := s.contextWithShutdown(ctx)
	defer cancel()
	models, effective, err := s.listModels(ctx, provider, false)
	if err != nil || ctx.Err() != nil || len(models) == 0 {
		return nil
	}
	return llm.MentionModelOptions(s.cfgRef, effective, models)
}
