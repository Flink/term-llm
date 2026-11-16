package engine

import (
	"context"
	"io"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

type terminationRecordingLogger struct{ events []core.Event }

func (*terminationRecordingLogger) LogRequest(string, string, core.Request)          {}
func (*terminationRecordingLogger) LogTurnRequest(int, string, string, core.Request) {}
func (l *terminationRecordingLogger) LogEvent(event core.Event)                      { l.events = append(l.events, event) }

func TestEngineLogsProviderTerminationReason(t *testing.T) {
	for _, mode := range []string{"simple", "loop", "tool_turn"} {
		for _, reason := range []string{"stop", "length", ""} {
			name := mode + "/" + reason
			t.Run(name, func(t *testing.T) {
				provider := &fakeProvider{script: func(call int, req core.Request) []core.Event {
					if mode == "tool_turn" && call == 0 {
						return []core.Event{{Type: core.EventToolCall, Tool: &core.ToolCall{ID: "call_1", Name: "guardian_recording", Arguments: []byte(`{}`)}}, {Type: core.EventDone, TerminationReason: "tool_calls"}}
					}
					return []core.Event{{Type: core.EventReasoningDelta, Text: "thinking"}, {Type: core.EventTextDelta, Text: "hello"}, {Type: core.EventDone, TerminationReason: reason}}
				}}
				registry := NewToolRegistry()
				req := core.Request{Messages: []core.Message{core.UserText("hi")}}
				if mode != "simple" {
					tool := guardianRecordingTool{}
					if err := registry.Register(tool); err != nil {
						t.Fatal(err)
					}
					req.Tools = []core.ToolSpec{tool.Spec()}
				}
				engine := NewEngine(provider, registry)
				logger := &terminationRecordingLogger{}
				engine.SetDebugLogger(logger)
				s, err := engine.Stream(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				done := 0
				for {
					event, err := s.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if event.Type == core.EventError {
						t.Fatal(event.Err)
					}
					if event.Type == core.EventDone {
						done++
						if event.TerminationReason != reason {
							t.Fatalf("reason = %q, want %q", event.TerminationReason, reason)
						}
					}
				}
				if done != 1 {
					t.Fatalf("done events = %d, want 1", done)
				}
				var logged []core.Event
				for _, event := range logger.events {
					if event.Type == core.EventDone {
						logged = append(logged, event)
					}
				}
				if len(logged) != 1 || logged[0].TerminationReason != reason {
					t.Fatalf("logged completions = %+v", logged)
				}
			})
		}
	}
}
