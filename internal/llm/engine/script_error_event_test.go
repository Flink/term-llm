package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
	corestream "github.com/term-llm/term-llm/internal/llm/core/stream"
)

func TestScriptControlErrorEventMatchesOutcome(t *testing.T) {
	for _, name := range []string{codemode.ExecToolName, codemode.WaitToolName, codemode.CancelToolName, codemode.LegacyToolName, "unrelated"} {
		for _, failure := range []string{"validation", "execute", "denied", "unregistered", "panic"} {
			for _, nested := range []bool{false, true} {
				label := name + "/" + failure
				if nested {
					label += "/nested"
				}
				t.Run(label, func(t *testing.T) {
					e := newCodeEngine()
					t.Cleanup(e.CloseCodeMode)
					if failure != "unregistered" {
						if err := e.RegisterTool(codeTestTool{name: name, run: func(_ context.Context, args json.RawMessage) (core.ToolOutput, error) {
							switch failure {
							case "validation":
								var decoded map[string]any
								return core.ToolOutput{}, json.Unmarshal(args, &decoded)
							case "panic":
								panic("script control panic")
							default:
								return core.ToolOutput{}, errors.New("script control failure")
							}
						}}); err != nil {
							t.Fatal(err)
						}
					}
					if failure == "denied" {
						e.SetAllowedToolsFilter([]string{})
					}
					ctx := context.Background()
					if nested {
						ctx = context.WithValue(ctx, nestedContextKey{}, &nestedLease{parent: "outer", finish: func() {}})
					}
					events := make(chan core.Event, 8)
					call := core.ToolCall{ID: "failed-call", Name: name, Arguments: json.RawMessage(`{}`), ToolInfo: "preview"}
					if failure == "validation" {
						call.Arguments = json.RawMessage(`{"invalid":`)
					}
					outcome := e.executeSingleToolCallOutcomeSafe(ctx, call, corestream.NewSender(ctx, events), false, false)
					if outcome.err == nil {
						t.Fatal("expected failed outcome")
					}
					message := outcome.message()
					want := message.Parts[0].ToolResult.Content
					if !strings.HasPrefix(want, "Error: ") {
						t.Fatalf("expected durable error content, got %q", want)
					}
					// Missing nested registrations return before an execution is announced.
					if nested && failure == "unregistered" {
						if len(events) != 0 {
							t.Fatal("nested unregistered execution unexpectedly emitted an event")
						}
						return
					}
					select {
					case event := <-events:
						if event.Type != core.EventToolExecEnd || event.ToolSuccess || event.ToolCallID != call.ID {
							t.Fatalf("unexpected terminal event: %+v", event)
						}
						if nested || name == codemode.LegacyToolName || name == "unrelated" {
							want = ""
						}
						if event.ToolOutput != want {
							t.Errorf("terminal output = %q, want %q", event.ToolOutput, want)
						}
						if len(event.ToolContentParts) != 0 {
							t.Fatal("failure unexpectedly forwarded content parts")
						}
					default:
						t.Fatal("missing terminal event")
					}
				})
			}
		}
	}
}
