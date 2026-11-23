package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestEngineBoundsReplayOnlyFollowups(t *testing.T) {
	for _, tc := range []struct {
		name     string
		turns    []string
		wantWarn bool
	}{
		{"stalls", []string{"replay", "replay"}, true},
		{"answers_on_followup", []string{"replay", "answer"}, false},
		{"tool_resets_allowance", []string{"replay", "tool", "replay", "answer"}, false},
		{"hosted_tool_resets_allowance", []string{"replay", "activity", "replay", "answer"}, false},
		{"steering_is_delivered_before_automatic_followup", []string{"steer", "replay", "answer"}, false},
		{"steering_after_empty_final", []string{"steer_final", "replay", "answer"}, false},
		{"steering_rescues_stalled_turn", []string{"replay", "steer", "replay", "answer"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &countingTool{}
			registry := NewToolRegistry()
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
			provider := &fakeProvider{}
			e := NewEngine(provider, registry)
			provider.script = func(n int, req core.Request) []core.Event {
				var events []core.Event
				if n > 0 && n <= len(tc.turns) && strings.HasPrefix(tc.turns[n-1], "steer") {
					last := req.Messages[len(req.Messages)-1]
					if last.Role != core.RoleUser || core.MessageText(last) != "new input" {
						t.Errorf("request %d did not receive queued steering", n)
					}
				}
				kind := "replay"
				if n < len(tc.turns) {
					kind = tc.turns[n]
				}
				switch kind {
				case "steer", "steer_final", "replay", "activity":
					if strings.HasPrefix(kind, "steer") {
						e.Steer("new input")
					}
					if kind == "activity" {
						events = append(events, core.Event{Type: core.EventToolActivity, ToolActivity: &core.ToolActivity{ID: "ws", Name: core.WebSearchToolName, Status: core.ToolActivityCompleted}})
					}
					// Fresh opaque IDs on every response are not progress.
					raw := json.RawMessage(fmt.Sprintf(`{"type":"reasoning","id":"rs_%d"}`, n))
					events = append(events, core.Event{Type: core.EventProviderReplay, ProviderReplay: &core.ProviderReplayItem{Raw: raw}})
				case "tool":
					events = append(events, core.Event{Type: core.EventToolCall, Tool: &core.ToolCall{ID: "call", Name: tool.Spec().Name, Arguments: json.RawMessage(`{}`)}})
				case "answer":
					events = append(events, core.Event{Type: core.EventTextDelta, Text: "done"})
				}
				return append(events, core.Event{Type: core.EventDone, TerminationReason: "completed", HasFinalAnswer: kind == "steer_final" || kind == "tool"})
			}
			var persistedReplay []string
			e.SetTurnCompletedCallback(func(_ context.Context, _ int, msgs []core.Message, _ core.TurnMetrics) error {
				for _, msg := range msgs {
					for _, part := range msg.Parts {
						if part.ProviderReplay != nil {
							persistedReplay = append(persistedReplay, string(part.ProviderReplay.Raw))
						}
					}
				}
				return nil
			})
			s, err := e.Stream(context.Background(), core.Request{Messages: []core.Message{core.UserText("start")}, Tools: []core.ToolSpec{tool.Spec()}, MaxTurns: 7})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var done, warnings int
			var text string
			for {
				event, err := s.Recv()
				if err == io.EOF {
					break
				}
				if err != nil || event.Type == core.EventError {
					t.Fatalf("stream error: %v / %v", err, event.Err)
				}
				switch event.Type {
				case core.EventPhase:
					if strings.HasPrefix(event.Text, core.WarningPhasePrefix) {
						warnings++
					}
				case core.EventDone:
					done++
					if event.TerminationReason != "completed" {
						t.Errorf("termination = %q, want completed", event.TerminationReason)
					}
				case core.EventTextDelta:
					text += event.Text
				}
			}
			if got := len(provider.calls); got != len(tc.turns) || done != 1 {
				t.Errorf("provider calls = %d, done events = %d; want %d, 1", got, done, len(tc.turns))
			}
			if tc.wantWarn {
				if warnings != 1 || text != "" || len(persistedReplay) != 2 || persistedReplay[0] == persistedReplay[1] {
					t.Errorf("stalled turn: warnings=%d text=%q replay=%q", warnings, text, persistedReplay)
				}
			} else if warnings != 0 || text != "done" {
				t.Errorf("progressing turn: warnings=%d text=%q", warnings, text)
			}
		})
	}
}
