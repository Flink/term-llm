package clibin

import (
	"encoding/json"
	"strings"

	"github.com/term-llm/term-llm/internal/llm/core"
)

// claudeContextReport tracks the context occupancy Claude Code reports while
// it runs its own tool loop. It is display-only: Claude Code owns compaction
// and term-llm never acts on these numbers.
//
// Each top-level `assistant` stream event carries the usage of the API call
// that produced it, so its prompt size (input + cache read + cache write) is
// the live context size at every tool-call checkpoint. The terminal `result`
// usage is summed across every call in the turn and is unusable for this;
// only its modelUsage[*].contextWindow is taken from it.
type claudeContextReport struct {
	selector string // Exact --model argument; empty means the CLI default
	model    string // Actual top-level assistant model, never the configured selector
	window   int    // Verified result window for this selector and model, if known

	sawModel       bool   // This subprocess has identified its main assistant model
	modelMessageID string // Latest assistant message that identified the model
	usageMessageID string // Assistant message the last reading came from
	used           int    // Prompt tokens of the latest measured top-level API call
}

func (r *claudeContextReport) useSelector(selector string) {
	// The same wire model can serve different selectors (e.g. opus and
	// opus[1m]) with different windows.
	if selector != r.selector {
		*r = claudeContextReport{selector: selector}
	}
}

func (r *claudeContextReport) beginTurn() {
	// With no --model, CLI settings can select a different window variant
	// without changing the wire model ID. Require a fresh result on that path.
	if r.selector == "" {
		*r = claudeContextReport{}
		return
	}
	*r = claudeContextReport{selector: r.selector, model: r.model, window: r.window}
}

func (r *claudeContextReport) restoreWindow(model string, window int, selector string) {
	*r = claudeContextReport{}
	if window > 0 && isClaudeAssistantModel(model) && isClaudeAssistantModel(selector) {
		r.selector, r.model, r.window = selector, model, window
	}
}

func isClaudeAssistantModel(model string) bool {
	// Claude Code may emit local placeholder messages without invoking a model.
	return model != "<synthetic>" && validClaudeModelID(model)
}

type claudeAssistantUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type claudeAssistantContextLine struct {
	ParentToolUseID string `json:"parent_tool_use_id"`
	Message         struct {
		ID    string                `json:"id"`
		Model string                `json:"model"`
		Usage *claudeAssistantUsage `json:"usage"`
	} `json:"message"`
}

type claudeModelUsage struct {
	ContextWindow  int    `json:"contextWindow"`
	CanonicalModel string `json:"canonicalModel"`
}

type claudeResultContextLine struct {
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
}

// observe updates the report from one stream-json line and returns the
// reading to publish, or nil when the line carries nothing new.
func (r *claudeContextReport) observe(lineType, line string) *core.ContextUsage {
	switch lineType {
	case "assistant":
		return r.observeAssistant(line)
	case "result":
		return r.observeResult(line)
	}
	return nil
}

func (r *claudeContextReport) observeAssistant(line string) *core.ContextUsage {
	var msg claudeAssistantContextLine
	// Subagent (sidechain) calls run in their own context window.
	if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.ParentToolUseID != "" {
		return nil
	}
	id, model := msg.Message.ID, msg.Message.Model
	if model == "" {
		// A partial can omit the model. Only attribute its usage when we
		// already saw a modeled frame for the same message in this turn.
		if id == "" || id != r.modelMessageID {
			return nil
		}
	} else if isClaudeAssistantModel(model) {
		if model != r.model {
			// Do not label the previous model's prompt size with a fallback window.
			*r = claudeContextReport{selector: r.selector, model: model}
		}
		r.sawModel, r.modelMessageID = true, id
	} else {
		return nil
	}
	if msg.Message.Usage == nil {
		return nil
	}
	u := msg.Message.Usage
	used := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	if used <= 0 {
		return nil
	}
	// Claude Code repeats the message usage on every content block event of
	// the same API call.
	if id != "" && id == r.usageMessageID && used == r.used {
		return nil
	}
	r.usageMessageID, r.used = id, used
	return r.reading()
}

func (r *claudeContextReport) observeResult(line string) *core.ContextUsage {
	if !r.sawModel {
		return nil // A result alone may describe helper calls, not the main model.
	}
	var msg claudeResultContextLine
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil
	}
	window := msg.windowFor(r.model)
	if window <= 0 || window == r.window {
		return nil
	}
	r.window = window
	if r.used <= 0 {
		return nil
	}
	return r.reading()
}

// Claude Code keys modelUsage by raw model ID. An alias or [1m] selector
// may differ from the model in the assistant message. Only accept an
// indirect match when it identifies one entry; side calls have windows too.
func (msg claudeResultContextLine) windowFor(model string) int {
	if usage, ok := msg.ModelUsage[model]; ok {
		return usage.ContextWindow
	}
	base := strings.TrimSuffix(model, "[1m]")
	window, found := 0, false
	for key, usage := range msg.ModelUsage {
		if strings.TrimSuffix(key, "[1m]") != base && strings.TrimSuffix(usage.CanonicalModel, "[1m]") != base {
			continue
		}
		if found {
			return 0
		}
		window, found = usage.ContextWindow, true
	}
	return window
}

func (r *claudeContextReport) reading() *core.ContextUsage {
	return &core.ContextUsage{UsedTokens: r.used, Window: r.window}
}

// reportClaudeContext publishes a context reading derived from line. Ephemeral
// one-shot requests run outside the conversation and never report.
func (p *ClaudeBinProvider) reportClaudeContext(lineType, line string, ephemeral bool, send eventSender) error {
	if ephemeral {
		return nil
	}
	reading := p.contextReport.observe(lineType, line)
	if reading == nil {
		return nil
	}
	return send.Send(core.Event{Type: core.EventContextUsage, Context: reading})
}
