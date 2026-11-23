package engine

import (
	"context"
	"sync/atomic"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
)

func isAsyncScriptControl(name string) bool {
	switch name {
	case codemode.ExecToolName, codemode.WaitToolName, codemode.CancelToolName:
		return true
	default:
		return false
	}
}

// scriptDelivery arbitrates the handoff between a worker and its collector.
// Once an engine-managed script control starts, the collector must take its
// outcome: Tool.Execute can already have drained a page by the time it returns.
// Before that point the collector can abandon the call without waiting for it.
// The control still runs with the original cancellable context.
type scriptDelivery struct{ state atomic.Uint32 }
type scriptDeliveryKey struct{}

const (
	scriptDeliveryStarted   = 1
	scriptDeliveryAbandoned = 2
)

func startScriptDelivery(ctx context.Context, d *scriptDelivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d != nil && !d.state.CompareAndSwap(0, scriptDeliveryStarted) {
		return context.Canceled
	}
	return nil
}

func (d *scriptDelivery) mustCollect() bool {
	return d != nil && !d.state.CompareAndSwap(0, scriptDeliveryAbandoned) && d.state.Load() == scriptDeliveryStarted
}

func cooperativeScriptControl(ctx context.Context, tool core.Tool) bool {
	control, ok := tool.(core.CodeModeLifecycleTool)
	return ok && control.IsCodeModeLifecycleTool() && isAsyncScriptControl(tool.Spec().Name) &&
		hasEngineScriptController(ctx) && !nestedExecution(ctx)
}

func hasEngineScriptController(ctx context.Context) bool {
	_, ok := core.CodeModeControllerFromContext(ctx).(*codeModeController)
	return ok
}
