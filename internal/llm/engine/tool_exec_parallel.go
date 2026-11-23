package engine

import (
	"context"
	"sync/atomic"

	"github.com/term-llm/term-llm/internal/llm/core"
	corestream "github.com/term-llm/term-llm/internal/llm/core/stream"
	"github.com/term-llm/term-llm/internal/restart"
)

type indexedToolOutcome struct {
	index   int
	outcome toolCallOutcome
}

func (e *Engine) executeParallelToolCalls(ctx context.Context, calls []core.ToolCall, send corestream.Sender, debug, debugRaw bool, outcomes []toolCallOutcome) ([]toolCallOutcome, error) {
	results := make(chan indexedToolOutcome, len(calls))
	deliveries := scriptBatchDeliveries(calls)
	// Admit the whole worker batch before any call can execute. A failed
	// admission must not leave an earlier worker with an uncollected result.
	type workerAdmission struct {
		ctx     context.Context
		release func()
	}
	workers := make([]workerAdmission, 0, maxParallelToolWorkers(len(calls)))
	for range cap(workers) {
		workerCtx, release, err := restart.Child(ctx)
		if err != nil {
			for _, worker := range workers {
				worker.release()
			}
			return nil, err
		}
		workers = append(workers, workerAdmission{ctx: workerCtx, release: release})
	}

	var nextCall atomic.Uint32
	for _, worker := range workers {
		go func() {
			defer worker.release()
			for ctx.Err() == nil {
				idx := int(nextCall.Add(1)) - 1
				if idx >= len(calls) || ctx.Err() != nil {
					return
				}
				callCtx := worker.ctx
				if deliveries != nil && isAsyncScriptControl(calls[idx].Name) {
					callCtx = context.WithValue(callCtx, scriptDeliveryKey{}, &deliveries[idx])
				}
				results <- indexedToolOutcome{index: idx, outcome: e.executeSingleToolCallOutcomeSafe(callCtx, calls[idx], send, debug, debugRaw)}
			}
		}()
	}

	collectParallelOutcomes(ctx, calls, results, deliveries, outcomes)
	return outcomes, nil
}

func scriptBatchDeliveries(calls []core.ToolCall) []scriptDelivery {
	for _, call := range calls {
		if isAsyncScriptControl(call.Name) {
			return make([]scriptDelivery, len(calls))
		}
	}
	return nil
}

func collectParallelOutcomes(ctx context.Context, calls []core.ToolCall, results <-chan indexedToolOutcome, deliveries []scriptDelivery, outcomes []toolCallOutcome) {
	received := make([]bool, len(calls))
	collect := func(result indexedToolOutcome) {
		outcomes[result.index] = result.outcome
		received[result.index] = true
	}
	for range calls {
		select {
		case result := <-results:
			collect(result)
		case <-ctx.Done():
			// Drain already available results without waiting for unrelated calls.
			for queued := len(results); queued > 0; queued-- {
				collect(<-results)
			}
			pending := 0
			for i := range deliveries {
				if !received[i] && deliveries[i].mustCollect() {
					pending++
				}
			}
			for pending > 0 {
				result := <-results
				if deliveries[result.index].mustCollect() {
					pending--
				}
				// A genuine outcome received while joining a started control
				// still wins over a synthetic cancellation.
				collect(result)
			}
			for i, call := range calls {
				if !received[i] {
					outcomes[i] = toolCallOutcome{call: call, err: ctx.Err()}
				}
			}
			return
		}
	}
}
