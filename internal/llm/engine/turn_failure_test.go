package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/term-llm/term-llm/internal/llm/core"
	corestream "github.com/term-llm/term-llm/internal/llm/core/stream"
	"github.com/term-llm/term-llm/internal/restart"
)

func TestTerminalBridgeFailureJournalsOutsideStreamHandlers(t *testing.T) {
	tool := &continuationTool{}
	call := core.ToolCall{ID: "bridged", Name: tool.Spec().Name, Arguments: json.RawMessage(`{}`)}
	response := make(chan core.ToolExecutionResponse, 1)
	provider := &streamProvider{stream: &sliceStream{events: []core.Event{
		{Type: core.EventToolCall, Tool: &call, ToolResponse: response},
		{Type: core.EventToolCall, Tool: &core.ToolCall{Namespace: "unexpected"}},
	}}}
	e := NewEngine(provider, nil)
	if err := e.RegisterTool(tool); err != nil {
		t.Fatal(err)
	}
	var journals int
	e.SetTurnCompletedCallback(func(_ context.Context, turn int, batch []core.Message, _ core.TurnMetrics) error {
		journals++
		if turn != 0 || len(batch) != 2 || core.ToolResultTextContent(batch[1].Parts[0].ToolResult) != "effect complete" {
			t.Errorf("invalid bridge journal on turn %d: %+v", turn, batch)
		}
		select {
		case got := <-response:
			if got.Err != nil || got.Result.Content != "effect complete" {
				t.Errorf("bridge response = %+v", got)
			}
		default:
			t.Error("journal ran before the bridge response was delivered")
		}
		return nil
	})
	err := e.runLoop(context.Background(), core.Request{Tools: e.Tools().AllSpecs(), Messages: []core.Message{core.UserText("run")}}, corestream.Sender{})
	if err == nil || !strings.Contains(err.Error(), "without an active discovery planner") || journals != 1 || tool.calls.Load() != 1 {
		t.Fatalf("error = %v, journals = %d, executions = %d", err, journals, tool.calls.Load())
	}
}

func TestCommittedRecoverySuspendsAtNextTurnWithoutSearch(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		name := "async"
		if bridge {
			name = "bridge"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				coordinator := &restart.Coordinator{InterruptAfter: time.Millisecond}
				root, release, err := coordinator.Root(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				ctx, closeTask := coordinator.NewTask(root)
				defer closeTask()
				stop, err := coordinator.Bind(ctx, func(context.Context) error { return errors.New("fixture replacement") })
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
				tool := &continuationTool{}
				call := core.ToolCall{ID: "recovered", Name: tool.Spec().Name, Arguments: json.RawMessage(`{}`)}
				event := core.Event{Type: core.EventToolCall, Tool: &call}
				if bridge {
					event.ToolResponse = make(chan core.ToolExecutionResponse, 1)
				}
				var step context.Context
				provider := newLifecycleProvider(func(ctx context.Context, _ int, req core.Request) (core.Stream, error) {
					step = ctx
					if !req.Search {
						t.Error("search must be enabled for the first provider attempt")
					}
					return &sliceStream{events: []core.Event{event, {Type: core.EventError, Err: &corestream.StreamIncompleteError{}}}}, nil
				})
				e := NewEngine(provider, nil)
				if err := e.RegisterTool(tool); err != nil {
					t.Fatal(err)
				}
				// Interrupt after the recovered transcript is committed but before
				// either recovery path can open the next provider turn.
				e.SetTurnCompletedCallback(func(context.Context, int, []core.Message, core.TurnMetrics) error {
					coordinator.Request()
					<-step.Done()
					return nil
				})
				err = e.runLoop(ctx, core.Request{Model: "original", Search: true, MaxTurns: 3, Tools: e.Tools().AllSpecs(), Messages: []core.Message{core.UserText("run")}}, corestream.Sender{})
				var suspended *SuspendedError
				if !errors.As(err, &suspended) {
					t.Fatalf("wanted suspension, got %v", err)
				}
				saved := suspended.Continuation
				if saved.Turn != 1 || saved.Request.Search || !saved.Interrupted || !saved.DiscardPartial || len(saved.Pending) != 0 || !lastMessageHasToolResults(saved.Request.Messages) || tool.calls.Load() != 1 {
					t.Fatalf("bad recovered boundary: %+v; calls=%d", saved, tool.calls.Load())
				}
			})
		})
	}
}

func TestBridgeSettlementPreventsReactiveCompactionReplay(t *testing.T) {
	for _, eventError := range []bool{false, true} {
		name := "recv"
		if eventError {
			name = "event"
		}
		t.Run(name, func(t *testing.T) {
			cause := errors.New("maximum context length exceeded")
			tool := &continuationTool{}
			call := core.ToolCall{ID: "bridged", Name: tool.Spec().Name, Arguments: json.RawMessage(`{}`)}
			provider := newLifecycleProvider(func(_ context.Context, n int, _ core.Request) (core.Stream, error) {
				if n != 0 {
					t.Error("opened a compaction or retry stream after dispatching bridge work")
					return nil, errors.New("unexpected provider call")
				}
				return &terminalBridgeStream{
					event: core.Event{Type: core.EventToolCall, Tool: &call, ToolResponse: make(chan core.ToolExecutionResponse, 1)},
					err:   cause, eventError: eventError,
				}, nil
			})
			e := NewEngine(provider, nil)
			if err := e.RegisterTool(tool); err != nil {
				t.Fatal(err)
			}
			e.SetCompaction(10000, DefaultCompactionConfig())
			err := e.runLoop(context.Background(), core.Request{Tools: e.Tools().AllSpecs(), Messages: []core.Message{core.UserText("run")}}, corestream.Sender{})
			if !errors.Is(err, cause) || provider.callCount() != 1 || tool.calls.Load() != 1 {
				t.Fatalf("error = %v, attempts = %d, tool executions = %d", err, provider.callCount(), tool.calls.Load())
			}
		})
	}
}

type terminalBridgeStream struct {
	event      core.Event
	err        error
	eventError bool
}

func (s *terminalBridgeStream) Recv() (core.Event, error) {
	if s.event.Tool != nil {
		event := s.event
		s.event = core.Event{}
		return event, nil
	}
	if s.eventError {
		return core.Event{Type: core.EventError, Err: s.err}, nil
	}
	return core.Event{}, s.err
}

func (*terminalBridgeStream) Close() error { return nil }
