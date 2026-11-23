package engine

import (
	"errors"

	"github.com/term-llm/term-llm/internal/llm/core"
)

// journalFailedSyncTurn preserves settled outcomes already committed by a
// bridge even when its terminal error is not eligible for automatic recovery.
// Explicit continuation can then hydrate real results rather than synthesizing
// "side effects unknown".
func (e *Engine) journalFailedSyncTurn(r *engineTurnRecovery) {
	scriptObservation := r.hasScriptObservation != nil && *r.hasScriptObservation
	if len(*r.syncToolResults) == 0 || (r.turnCallback == nil && !scriptObservation) {
		return
	}
	var assistant core.Message
	if r.preserveInlineToolOrder && len(*r.inlineSyncParts) > 0 {
		assistant = r.buildOrderedInlineAssistant(*r.inlineSyncParts)
	} else {
		assistant = buildAssistantMessageWithReasoningMetadata(r.textBuilder.String(), e.withToolPreview(*r.syncToolCalls), r.reasoningBuilder.String(), *r.reasoningSummaryParts, *r.reasoningItemID, *r.reasoningEncryptedContent, *r.reasoningKind)
	}
	assistant = attachProviderReplayParts(assistant, *r.providerReplayParts)
	messages := append([]core.Message{assistant}, (*r.syncToolResults)...)
	r.syncJournaled = true
	if scriptObservation {
		// Keep the same boundary in any restart continuation. A callback alone
		// is insufficient when the replacement restores the engine transcript.
		r.req.Messages = append(r.req.Messages, messages...)
		r.advanceCommittedTurn(r.req)
	}
	if r.turnCallback == nil {
		return
	}
	metrics := *r.turnMetrics
	metrics.ToolCalls = len(*r.syncToolCalls)
	ctx, cancel := callbackContext(r.ctx)
	defer cancel()
	_ = r.turnCallback(ctx, r.attempt, messages, metrics)
}

// journalOnTerminalFailure covers every terminal exit after bridge dispatch,
// including failures in event processing and post-stream recovery.
func (s *providerTurnSyncTools) journalOnTerminalFailure(e *Engine, closeStream func(), r *engineTurnRecovery, turnErr *error) {
	err := *turnErr
	if err == nil || r.syncJournaled || !s.turn.syncToolsExecuted || errors.Is(err, errEngineTurnRetry) || errors.Is(err, errEngineTurnAdvance) {
		return
	}
	s.finishAfterStreamFailure(err)
	closeStream()
	e.journalFailedSyncTurn(r)
}
