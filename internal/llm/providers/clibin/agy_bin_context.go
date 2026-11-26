package clibin

import (
	"strings"

	"github.com/term-llm/term-llm/internal/llm/core"
)

type agyStepUpdate struct {
	ConversationID string    `json:"conversation_id"`
	StepIndex      *int      `json:"step_index"`
	State          string    `json:"state"`
	StepType       string    `json:"step_type"`
	TextDelta      string    `json:"text_delta"`
	Usage          *agyUsage `json:"usage"`
}

func (u agyUsage) toCore() core.Usage {
	return core.Usage{
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.CacheReadTokens,
		ReasoningTokens:   u.ThinkingTokens,
	}
}

func (u agyUsage) hasTokens() bool {
	return u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.ThinkingTokens > 0
}

func (s *agyStreamState) observeStepUsage(update agyStepUpdate, send eventSender) error {
	if !strings.EqualFold(strings.TrimSpace(update.State), "DONE") || update.Usage == nil || !update.Usage.hasTokens() {
		return nil
	}
	if update.StepIndex != nil {
		if s.seenUsageSteps == nil {
			s.seenUsageSteps = make(map[int]bool)
		}
		if s.seenUsageSteps[*update.StepIndex] {
			return nil
		}
		s.seenUsageSteps[*update.StepIndex] = true
	}

	u := update.Usage
	s.stepUsage.Add(u.toCore())

	if s.ephemeral || update.StepType != "agent_response" {
		return nil
	}
	used := u.InputTokens + u.CacheReadTokens
	if used <= 0 {
		return nil
	}
	return send.Send(core.Event{
		Type:    core.EventContextUsage,
		Context: &core.ContextUsage{UsedTokens: used, Window: s.resolveContextWindow()},
	})
}

func (s *agyStreamState) resolveContextWindow() int {
	if s.isolation == nil {
		return 0
	}
	genModel := s.isolation.LastGenerationModel()
	initModel, reqModel := s.initModel, s.requestedModel
	initWithEff := agyModelWithEffort(initModel, s.requestedEff)
	reqWithEff := agyModelWithEffort(reqModel, s.requestedEff)

	var candidates []string
	if s.exposeBridge {
		// On tool-enabled turns, LastGenerationModel only records verified
		// bridge-tool requests, so it accurately tracks the active main model
		// (including defaults, fallbacks, and orchestrator selections).
		candidates = []string{genModel, initWithEff, reqWithEff, initModel, reqModel}
	} else {
		// Without bridge-tool verification, prefer the declared init/requested
		// model before an unverified generation model (which could be auxiliary).
		candidates = []string{initWithEff, reqWithEff, initModel, reqModel, genModel}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if window := agyCatalogContextWindow(candidate, s.isolation.ModelContextWindow); window > 0 {
			return window
		}
	}
	return 0
}

// agyCatalogContextWindow prefers an exact ID. Without one, a bare model
// resolves only when all observed effort variants have the same window.
func agyCatalogContextWindow(model string, lookup func(string) int) int {
	if window := lookup(model); window > 0 {
		return window
	}
	if _, existing := parseAgyEffort(model); existing != "" {
		return 0
	}
	matched := 0
	for _, effort := range agyEffortLevels {
		window := lookup(model + "-" + effort)
		if window <= 0 {
			continue
		}
		if matched > 0 && matched != window {
			return 0
		}
		matched = window
	}
	return matched
}

func agyModelWithEffort(model, effort string) string {
	if model == "" || effort == "" {
		return ""
	}
	if _, existing := parseAgyEffort(model); existing != "" {
		return ""
	}
	return model + "-" + effort
}
