package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
	corestream "github.com/term-llm/term-llm/internal/llm/core/stream"
	"github.com/term-llm/term-llm/internal/restart"
)

// Put a barrier immediately after the real destructive drain, before the tool
// hands the rendered observation to the engine. synctest.Wait lets both engine
// cancellation selectors run before the test permits the result to be sent.
func TestScriptObservationSurvivesCancelledDelivery(t *testing.T) {
	for _, mode := range []string{"serial", "parallel", "bridge EOF", "bridge error", "bridge restart"} {
		t.Run(mode, func(t *testing.T) {
			if !codemode.Available() {
				t.Skip("code mode unavailable")
			}
			base := asyncCodeContext("delivery", "original-spawn")
			call := core.ToolCall{ID: "observation", Name: codemode.WaitToolName, Arguments: json.RawMessage(`{}`)}
			ordinary := core.ToolCall{ID: "ordinary", Name: "ordinary", Arguments: json.RawMessage(`{}`)}
			provider := &lifecycleProvider{}
			e := NewEngine(provider, nil)
			t.Cleanup(e.CloseCodeMode)
			started, releaseEffect := make(chan struct{}), make(chan struct{})
			var effects atomic.Int32
			effect := codeModeBlockingTool(make(chan struct{}, 1), releaseEffect)
			originalRun := effect.run
			effect.run = func(ctx context.Context, args json.RawMessage) (core.ToolOutput, error) {
				if effects.Add(1) == 1 {
					close(started)
				}
				return originalRun(ctx, args)
			}
			if err := e.RegisterTool(effect); err != nil {
				t.Fatal(err)
			}
			first, err := e.ExecCodeMode(base, `await tools.hold({}); text('abcd' + 'remaining');`, 0, 0)
			if err != nil || first.CellID == "" {
				t.Fatalf("start: %+v, %v", first, err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("script effect did not start")
			}
			close(releaseEffect)
			waitAsyncCellDone(t, e, first.CellID, "delivery")
			<-e.codeModeSessions.lookup(first.CellID, "delivery").capability.Done()

			synctest.Test(t, func(t *testing.T) {
				// Use a fresh sender and request context. The completed cell is still
				// owned by the original session and no JS runs inside the bubble.
				ctx, cancel := context.WithCancel(base)
				defer cancel()
				var reload *restart.Coordinator
				if mode == "bridge restart" {
					reload = &restart.Coordinator{InterruptAfter: time.Millisecond}
					root, release, err := reload.Root(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
					var closeTask func()
					ctx, closeTask = reload.NewTask(root)
					defer closeTask()
					stop, err := reload.Bind(ctx, func(context.Context) error { return errors.New("fixture replacement") })
					if err != nil {
						t.Fatal(err)
					}
					defer stop()
				}
				drained := make(chan core.CodeModeCallResult, 1)
				publish := make(chan struct{})
				if err := e.RegisterTool(lifecycleCodeTestTool{codeTestTool{name: call.Name, run: func(ctx context.Context, _ json.RawMessage) (core.ToolOutput, error) {
					page, err := core.CodeModeControllerFromContext(ctx).WaitCodeMode(ctx, first.CellID, 0, 1)
					drained <- page
					<-publish
					return core.ToolOutput{ContentParts: page.Parts, ChildCalls: page.Calls}, err
				}}}); err != nil {
					t.Fatal(err)
				}
				ordinaryStarted, releaseOrdinary := make(chan struct{}), make(chan struct{})
				defer close(releaseOrdinary)
				if err := e.RegisterTool(codeTestTool{name: ordinary.Name, run: func(context.Context, json.RawMessage) (core.ToolOutput, error) {
					close(ordinaryStarted)
					<-releaseOrdinary
					return core.TextOutput("late unrelated result"), nil
				}}); err != nil {
					t.Fatal(err)
				}
				var messages []core.Message
				var runErr error
				callbackCount := 0
				interrupted := make(chan struct{})
				provider.script = func(providerCtx context.Context, _ int, _ core.Request) (core.Stream, error) {
					switch mode {
					case "bridge EOF":
						return &scriptDeliveryBridgeStream{ctx: providerCtx, call: call, err: io.EOF}, nil
					case "bridge error", "bridge restart":
						return &scriptDeliveryBridgeStream{ctx: providerCtx, call: call, err: context.Canceled, interrupted: interrupted}, nil
					default:
						return &sliceStream{events: []core.Event{
							{Type: core.EventToolCall, Tool: &call},
							{Type: core.EventToolCall, Tool: &ordinary},
						}}, nil
					}
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					e.SetTurnCompletedCallback(func(cbCtx context.Context, _ int, batch []core.Message, _ core.TurnMetrics) error {
						if cbCtx.Err() != nil {
							t.Error("cancelled persistence callback context")
						}
						callbackCount++
						messages = append(messages, batch...)
						return nil
					})
					runErr = e.runLoop(ctx, core.Request{Tools: e.Tools().AllSpecs(), Messages: []core.Message{core.UserText("observe")}, ParallelToolCalls: mode == "parallel"}, corestream.Sender{})
				}()
				synctest.Wait()
				select {
				case page := <-drained:
					if partsText(page) != "abcd" || !page.OutputLimited || !page.HasMoreOutput || len(page.Calls) != 1 {
						t.Errorf("expected a drained first page and audit: %+v", page)
					}
				default:
					t.Error("tool did not reach the post-drain barrier")
				}
				if mode == "parallel" {
					select {
					case <-ordinaryStarted:
					default:
						t.Error("unrelated tool did not start in parallel")
					}
				}
				if reload != nil {
					reload.Request()
					// Blocking here lets synctest advance the restart grace timer.
					select {
					case <-interrupted:
					case <-time.After(time.Second):
						t.Error("provider step was not interrupted by restart")
					}
				} else {
					cancel()
				}
				synctest.Wait()
				select {
				case <-done:
					t.Error("engine returned before the drained observation could be delivered")
				default:
				}
				close(publish)
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Fatal("engine waited for an unrelated tool ignoring cancellation")
				}
				if callbackCount != 1 {
					t.Errorf("expected one persistence callback, got %d", callbackCount)
				}
				assertScriptDeliveryResult(t, messages, call.ID)
				if reload == nil {
					if !errors.Is(runErr, context.Canceled) {
						t.Errorf("expected cancellation, got %v", runErr)
					}
					return
				}
				var suspended *SuspendedError
				if !errors.As(runErr, &suspended) {
					t.Fatalf("expected suspension, got %v", runErr)
				}
				saved := suspended.Continuation
				if !saved.Interrupted || saved.Turn != 1 || len(saved.Pending) != 0 {
					t.Fatalf("expected interrupted continuation at the next turn with nothing to replay: %+v", saved)
				}
				assertScriptDeliveryResult(t, saved.Request.Messages, call.ID)

				replacement := NewEngine(newLifecycleProvider(func(_ context.Context, _ int, req core.Request) (core.Stream, error) {
					assertScriptDeliveryResult(t, req.Messages, call.ID)
					return &sliceStream{events: []core.Event{{Type: core.EventTextDelta, Text: "resumed"}}}, nil
				}), e.Tools())
				resumedTurn := -1
				replacement.SetTurnCompletedCallback(func(_ context.Context, turn int, _ []core.Message, _ core.TurnMetrics) error {
					resumedTurn = turn
					return nil
				})
				stream, err := replacement.Stream(base, core.Request{Resume: saved})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				for {
					event, err := stream.Recv()
					if err == io.EOF {
						break
					}
					if err != nil || event.Err != nil {
						t.Fatal(err, event.Err)
					}
				}
				if resumedTurn != 1 {
					t.Errorf("replacement completed turn %d, want 1", resumedTurn)
				}
			})

			next, err := e.WaitCodeMode(base, first.CellID, 0, 0)
			if err != nil || partsText(next) != "remaining" || next.HasMoreOutput || len(next.Calls) != 0 || effects.Load() != 1 {
				t.Fatalf("lost or repeated output/audit/effect: %+v, %v; effects %d", next, err, effects.Load())
			}
		})
	}
}

func assertScriptDeliveryResult(t *testing.T, messages []core.Message, callID string) {
	t.Helper()
	count := 0
	for _, message := range messages {
		for _, part := range message.Parts {
			if result := part.ToolResult; result != nil && result.ID == callID {
				count++
				if result.IsError || core.ToolResultTextContent(result) != "abcd" || len(result.ChildCalls) != 1 || result.ChildCalls[0].Name != "hold" {
					t.Errorf("expected the real observation, got %+v", result)
				}
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one observation, got %d in %+v", count, messages)
	}
}

type scriptDeliveryBridgeStream struct {
	ctx         context.Context
	call        core.ToolCall
	err         error
	sent        bool
	interrupted chan<- struct{}
}

func (s *scriptDeliveryBridgeStream) Recv() (core.Event, error) {
	if s.sent {
		<-s.ctx.Done()
		if s.interrupted != nil && interruptedForRestart(s.ctx) {
			close(s.interrupted)
		}
		return core.Event{}, s.err
	}
	s.sent = true
	// Deliberately not listening: the persistence path must preserve the result
	// even when a disconnected synchronous bridge can't receive it.
	return core.Event{Type: core.EventToolCall, Tool: &s.call, ToolResponse: make(chan core.ToolExecutionResponse)}, nil
}

func (*scriptDeliveryBridgeStream) Close() error { return nil }

func TestParallelCollectorKeepsResultsReceivedWhileJoiningScriptControl(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := []core.ToolCall{{ID: "started", Name: codemode.WaitToolName}, {ID: "ordinary"}, {ID: "unreturned"}}
		deliveries := scriptBatchDeliveries(calls)
		if err := startScriptDelivery(ctx, &deliveries[0]); err != nil {
			t.Fatal(err)
		}
		cancel()
		results := make(chan indexedToolOutcome, len(calls))
		outcomes := make([]toolCallOutcome, len(calls))
		done := make(chan struct{})
		go func() {
			collectParallelOutcomes(ctx, calls, results, deliveries, outcomes)
			close(done)
		}()
		synctest.Wait() // Cancelled collector is now waiting for the started control.
		results <- indexedToolOutcome{index: 1, outcome: toolCallOutcome{call: calls[1], output: core.TextOutput("genuine")}}
		results <- indexedToolOutcome{index: 0, outcome: toolCallOutcome{call: calls[0], output: core.TextOutput("observation")}}
		<-done
		if outcomes[0].output.Content != "observation" || outcomes[1].output.Content != "genuine" || !errors.Is(outcomes[2].err, context.Canceled) {
			t.Fatalf("unexpected collected outcomes: %+v", outcomes)
		}
	})
}
