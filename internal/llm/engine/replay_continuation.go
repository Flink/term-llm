package engine

import "github.com/term-llm/term-llm/internal/llm/core"

type replayDisposition uint8

const (
	replayStop replayDisposition = iota
	replayToolWork
	replayProbe
	replayStalled
)

// replayDisposition distinguishes work for the next turn from history that
// merely has to be retained. An opaque item (even one with a fresh ID) gets at
// most one speculative follow-up, never one after an explicit final answer.
func (c *engineTurnCompletion) replayDisposition() replayDisposition {
	if partsContainDiscoveryCall(c.providerReplayParts) {
		// The provider has not yet received the client discovery result, even if
		// it emitted commentary or a final answer alongside the call.
		return replayToolWork
	}
	if c.hasFinalAnswer || c.textBuilder.Len() != 0 || len(c.providerReplayParts) == 0 {
		return replayStop
	}
	for _, part := range c.providerReplayParts {
		if part.Type == core.PartToolActivity && part.ToolActivity != nil {
			return replayToolWork // Actual provider-managed tool work, not just replay.
		}
	}
	if c.replayFollowup {
		return replayStalled
	}
	return replayProbe
}

func (e *Engine) settleTextResponse(c *engineTurnCompletion, req *core.Request, msg core.Message) error {
	replay := c.replayDisposition()
	canContinue := c.attempt < c.maxTurns-1
	advance := canContinue && (replay == replayProbe || replay == replayToolWork)
	if len(msg.Parts) > 0 {
		if !advance {
			c.compaction.maybeAfterResponse([]core.Message{msg})
		}
		if c.turnCallback != nil {
			ctx, cancel := callbackContext(c.ctx)
			_ = c.turnCallback(ctx, c.attempt, []core.Message{msg}, c.turnMetrics)
			cancel()
		}
	}
	canAcceptMore := c.attempt < c.maxTurns-2
	var steering []queuedSteering
	if advance {
		steering = e.drainSteeringForNextTurn(canAcceptMore)
	} else {
		steering = e.drainBoundarySteering(canContinue, canAcceptMore)
	}
	if !advance && len(steering) == 0 {
		if replay == replayStalled {
			if err := c.send.Send(core.Event{Type: core.EventPhase, Text: core.WarningPhasePrefix + "Model returned no answer or tool work on two consecutive turns; stopping."}); err != nil {
				return err
			}
		}
		return c.sendDone()
	}
	if len(msg.Parts) > 0 {
		req.Messages = append(req.Messages, msg)
	}
	if err := appendTurnSteering(c.ctx, c.send, req, c.turnCallback, c.attempt, steering); err != nil {
		return err
	}
	if err := e.applyPendingRequestModelSwitch(c.ctx, req, c.send, c.runID, c.modelSwitchOrdinal, c.attempt+1); err != nil {
		return err
	}
	if len(steering) == 0 && replay == replayProbe {
		return errEngineReplayFollowup
	}
	return errEngineTurnAdvance
}
