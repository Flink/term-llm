package clibin

import (
	"context"
	"slices"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestAgyContextUsageEmitsLivePromptAndCatalogWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan core.Event, 16)
	send := eventSender{ctx: ctx, ch: events}
	isolation := &fakeAgyIsolation{
		started: true,
		windows: map[string]int{
			"gemini-4-neon-high": 1048576,
			"gemini-4-neon-low":  1048576,
		},
		lastModel: "gemini-4-neon-high",
	}
	state := &agyStreamState{
		isolation:      isolation,
		exposeBridge:   true,
		requestedModel: "gemini-4-neon",
		requestedEff:   "high",
	}

	lines := []string{
		`{"event":"init","conversation_id":"c1","init":{"model":"gemini-4-neon"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":0,"state":"DONE","step_type":"user_input"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":8734,"output_tokens":91,"thinking_tokens":46,"cache_read_tokens":0,"total_tokens":8825}}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"tool","tool_name":"shell"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"ACTIVE","step_type":"agent_response","text_delta":"DONE"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"\n","usage":{"input_tokens":2236,"output_tokens":12,"thinking_tokens":10,"cache_read_tokens":6677,"total_tokens":2248}}}`,
		// Repeated DONE line for the same step_index must not duplicate context or turn usage.
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":2236,"output_tokens":12,"thinking_tokens":10,"cache_read_tokens":6677,"total_tokens":2248}}}`,
		`{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"DONE\n","usage":{"input_tokens":10970,"output_tokens":103,"thinking_tokens":56,"cache_read_tokens":6677}}}`,
	}
	for _, line := range lines {
		if err := handleAgyStreamLine(line, send, state); err != nil {
			t.Fatalf("handleAgyStreamLine(%s): %v", line, err)
		}
	}
	close(events)

	var gotContext []core.ContextUsage
	for ev := range events {
		if ev.Type == core.EventContextUsage && ev.Context != nil {
			gotContext = append(gotContext, *ev.Context)
		}
	}
	wantContext := []core.ContextUsage{
		{UsedTokens: 8734, Window: 1048576},
		{UsedTokens: 8913, Window: 1048576},
	}
	if !slices.Equal(gotContext, wantContext) {
		t.Fatalf("context readings = %+v, want %+v", gotContext, wantContext)
	}
	wantUsage := core.Usage{
		InputTokens:       10970,
		OutputTokens:      103,
		CachedInputTokens: 6677,
		ReasoningTokens:   56,
	}
	if state.usage == nil || *state.usage != wantUsage {
		t.Fatalf("turn usage = %+v, want %+v", state.usage, wantUsage)
	}
}

func TestAgyResumedTurnUsesStepDeltaInsteadOfCumulativeResultUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan core.Event, 8)
	send := eventSender{ctx: ctx, ch: events}
	isolation := &fakeAgyIsolation{
		started:   true,
		windows:   map[string]int{"gemini-4-neon-low": 1048576},
		lastModel: "gemini-4-neon-low",
	}
	state := &agyStreamState{
		expectedConversationID: "c1",
		isolation:              isolation,
		exposeBridge:           true,
		requestedModel:         "gemini-4-neon",
		requestedEff:           "low",
	}

	// Mirrors a real resumed turn: step 6 is the only call in turn 2, while
	// result.usage carries the cumulative total across turn 1 + turn 2.
	lines := []string{
		`{"event":"init","conversation_id":"c1","init":{"model":"gemini-4-neon"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":6,"state":"DONE","step_type":"agent_response","text_delta":"OK2\n","usage":{"input_tokens":2265,"output_tokens":15,"thinking_tokens":12,"cache_read_tokens":6864,"total_tokens":2280}}}`,
		`{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"OK2\n","usage":{"input_tokens":13235,"output_tokens":118,"thinking_tokens":68,"cache_read_tokens":13541}}}`,
	}
	for _, line := range lines {
		if err := handleAgyStreamLine(line, send, state); err != nil {
			t.Fatal(err)
		}
	}
	close(events)

	wantUsage := core.Usage{
		InputTokens:       2265,
		OutputTokens:      15,
		CachedInputTokens: 6864,
		ReasoningTokens:   12,
	}
	if state.usage == nil || *state.usage != wantUsage {
		t.Fatalf("resumed turn usage = %+v, want delta %+v", state.usage, wantUsage)
	}
	var gotContext []core.ContextUsage
	for ev := range events {
		if ev.Type == core.EventContextUsage && ev.Context != nil {
			gotContext = append(gotContext, *ev.Context)
		}
	}
	wantContext := []core.ContextUsage{{UsedTokens: 9129, Window: 1048576}}
	if !slices.Equal(gotContext, wantContext) {
		t.Fatalf("context readings = %+v, want %+v", gotContext, wantContext)
	}
}

func TestAgyContextUsageUnknownModelAndEphemeralSuppression(t *testing.T) {
	t.Run("unknown model reports zero window", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan core.Event, 4)
		send := eventSender{ctx: ctx, ch: events}
		state := &agyStreamState{
			isolation:      &fakeAgyIsolation{started: true, windows: map[string]int{"other-model": 128000}},
			requestedModel: "gemini-4-neon",
			requestedEff:   "high",
		}
		line := `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":500,"cache_read_tokens":200,"output_tokens":10}}}`
		if err := handleAgyStreamLine(line, send, state); err != nil {
			t.Fatal(err)
		}
		close(events)
		ev := <-events
		if ev.Type != core.EventContextUsage || ev.Context == nil || *ev.Context != (core.ContextUsage{UsedTokens: 700, Window: 0}) {
			t.Fatalf("event = %+v, want UsedTokens=700 Window=0", ev)
		}
	})

	t.Run("ephemeral request never reports context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan core.Event, 4)
		send := eventSender{ctx: ctx, ch: events}
		state := &agyStreamState{
			isolation: &fakeAgyIsolation{started: true, windows: map[string]int{"gemini-4-neon-high": 1048576}, lastModel: "gemini-4-neon-high"},
			ephemeral: true,
		}
		line := `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":500,"cache_read_tokens":200,"output_tokens":10}}}`
		if err := handleAgyStreamLine(line, send, state); err != nil {
			t.Fatal(err)
		}
		close(events)
		for ev := range events {
			if ev.Type == core.EventContextUsage {
				t.Fatalf("ephemeral request emitted EventContextUsage: %+v", ev.Context)
			}
		}
	})

	t.Run("toolless turn prefers init model over unverified last generation model", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan core.Event, 4)
		send := eventSender{ctx: ctx, ch: events}
		state := &agyStreamState{
			isolation: &fakeAgyIsolation{
				started: true,
				windows: map[string]int{
					"claude-opus-5-5-high": 1000000,
					"gemini-3-flash":       1048576,
				},
				lastModel: "gemini-3-flash",
			},
			exposeBridge:   false,
			requestedModel: "claude-opus-5-5",
			requestedEff:   "high",
		}
		if err := handleAgyStreamLine(`{"event":"init","conversation_id":"c1","init":{"model":"claude-opus-5-5"}}`, send, state); err != nil {
			t.Fatal(err)
		}
		if err := handleAgyStreamLine(`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":500,"cache_read_tokens":200,"output_tokens":10}}}`, send, state); err != nil {
			t.Fatal(err)
		}
		close(events)
		ev := <-events
		if ev.Type != core.EventContextUsage || ev.Context == nil || *ev.Context != (core.ContextUsage{UsedTokens: 700, Window: 1000000}) {
			t.Fatalf("event = %+v, want Window=1000000 from claude-opus-5-5-high", ev)
		}
	})

	t.Run("toolless default turn falls back to generation model", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan core.Event, 4)
		send := eventSender{ctx: ctx, ch: events}
		state := &agyStreamState{
			isolation: &fakeAgyIsolation{
				started:   true,
				windows:   map[string]int{"gemini-4-neon-high": 1048576},
				lastModel: "gemini-4-neon-high",
			},
			exposeBridge: false,
		}
		if err := handleAgyStreamLine(`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":500,"cache_read_tokens":200,"output_tokens":10}}}`, send, state); err != nil {
			t.Fatal(err)
		}
		close(events)
		ev := <-events
		if ev.Type != core.EventContextUsage || ev.Context == nil || *ev.Context != (core.ContextUsage{UsedTokens: 700, Window: 1048576}) {
			t.Fatalf("event = %+v, want Window=1048576 from LastGenerationModel", ev)
		}
	})
}

func TestAgyResolveContextWindowEffortVariants(t *testing.T) {
	isolation := &fakeAgyIsolation{windows: map[string]int{
		"gemini-4-neon-low":  1048576,
		"gemini-4-neon-high": 1048576,
		"mixed-window-low":   128000,
		"mixed-window-high":  256000,
		"exact-window":       512000,
		"exact-window-low":   128000,
		"exact-window-high":  256000,
	}}
	for _, tc := range []struct {
		name, model, effort string
		want                int
	}{
		{name: "variants agree", model: "gemini-4-neon", want: 1048576},
		{name: "variants disagree", model: "mixed-window", want: 0},
		{name: "explicit effort", model: "mixed-window", effort: "high", want: 256000},
		{name: "exact ID wins", model: "exact-window", want: 512000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := agyStreamState{isolation: isolation, requestedModel: tc.model, requestedEff: tc.effort}
			if got := state.resolveContextWindow(); got != tc.want {
				t.Fatalf("resolveContextWindow() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAgyStepUsageEdgeCases(t *testing.T) {
	t.Run("fresh turn prefers exact result usage over partial step sum", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan core.Event, 8)
		send := eventSender{ctx: ctx, ch: events}
		state := &agyStreamState{}
		lines := []string{
			`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":500,"output_tokens":10,"cache_read_tokens":100}}}`,
			`{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"ok","usage":{"input_tokens":800,"output_tokens":25,"thinking_tokens":5,"cache_read_tokens":300}}}`,
		}
		for _, line := range lines {
			if err := handleAgyStreamLine(line, send, state); err != nil {
				t.Fatal(err)
			}
		}
		want := core.Usage{InputTokens: 800, OutputTokens: 25, CachedInputTokens: 300, ReasoningTokens: 5}
		if state.usage == nil || *state.usage != want {
			t.Fatalf("fresh turn usage = %+v, want %+v", state.usage, want)
		}
	})

	t.Run("missing step_index does not collapse distinct steps and empty usage does not shadow real usage", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan core.Event, 8)
		send := eventSender{ctx: ctx, ch: events}
		state := &agyStreamState{expectedConversationID: "c1"}
		lines := []string{
			// Empty usage DONE for step 2 must not prevent later real usage for step 2.
			`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"agent_response","usage":{}}}`,
			`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":100,"output_tokens":5,"cache_read_tokens":50}}}`,
			// Two steps omitting step_index must both be counted.
			`{"event":"step_update","step_update":{"conversation_id":"c1","state":"DONE","step_type":"agent_response","usage":{"input_tokens":200,"output_tokens":7,"cache_read_tokens":60}}}`,
			`{"event":"step_update","step_update":{"conversation_id":"c1","state":"DONE","step_type":"agent_response","usage":{"input_tokens":300,"output_tokens":9,"cache_read_tokens":70}}}`,
			`{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"ok","usage":{"input_tokens":9999,"output_tokens":999,"cache_read_tokens":9999}}}`,
		}
		for _, line := range lines {
			if err := handleAgyStreamLine(line, send, state); err != nil {
				t.Fatal(err)
			}
		}
		want := core.Usage{InputTokens: 600, OutputTokens: 21, CachedInputTokens: 180}
		if state.usage == nil || *state.usage != want {
			t.Fatalf("resumed usage = %+v, want %+v", state.usage, want)
		}
	})

	t.Run("agyModelWithEffort skips models that already carry an effort suffix", func(t *testing.T) {
		if got := agyModelWithEffort("gemini-4-neon-high", "low"); got != "" {
			t.Fatalf("agyModelWithEffort(gemini-4-neon-high, low) = %q, want empty", got)
		}
		if got := agyModelWithEffort("gemini-4-neon", "high"); got != "gemini-4-neon-high" {
			t.Fatalf("agyModelWithEffort(gemini-4-neon, high) = %q, want gemini-4-neon-high", got)
		}
	})
}

func TestAgyRunCommandPlumbsContextAndEphemeralOptions(t *testing.T) {
	binary := writeAgyTestScript(t, `
IFS= read -r _
printf '%s\n' '{"event":"init","conversation_id":"conv-1","init":{"model":"gemini-4-neon"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":4,"state":"DONE","step_type":"agent_response","text_delta":"ok","usage":{"input_tokens":2000,"output_tokens":15,"thinking_tokens":10,"cache_read_tokens":6000}}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"ok","usage":{"input_tokens":12000,"output_tokens":115,"thinking_tokens":60,"cache_read_tokens":12000}}}'
`)
	p := newAgyTransportProvider(t, binary, nil)
	p.isolation = &fakeAgyIsolation{
		windows: map[string]int{"gemini-4-neon-high": 1_048_576},
	}

	ctx := context.Background()
	t.Run("resumed turn emits context usage and step-delta billing usage", func(t *testing.T) {
		events := make(chan core.Event, 16)
		if _, err := p.runCommand(ctx, nil, agyRunOptions{
			prompt:                 "hi",
			expectedConversationID: "conv-1",
			requestedModel:         "gemini-4-neon",
			requestedEff:           "high",
		}, eventSender{ctx: ctx, ch: events}); err != nil {
			t.Fatal(err)
		}
		close(events)

		var gotContext []core.ContextUsage
		var gotUsage *core.Usage
		for ev := range events {
			switch ev.Type {
			case core.EventContextUsage:
				gotContext = append(gotContext, *ev.Context)
			case core.EventUsage:
				gotUsage = ev.Use
			}
		}
		wantContext := core.ContextUsage{UsedTokens: 8000, Window: 1_048_576}
		if len(gotContext) != 1 || gotContext[0] != wantContext {
			t.Fatalf("context events = %+v, want [%+v]", gotContext, wantContext)
		}
		wantUsage := core.Usage{InputTokens: 2000, OutputTokens: 15, CachedInputTokens: 6000, ReasoningTokens: 10}
		if gotUsage == nil || *gotUsage != wantUsage {
			t.Fatalf("usage event = %+v, want %+v", gotUsage, wantUsage)
		}
	})

	t.Run("ephemeral option suppresses context usage", func(t *testing.T) {
		events := make(chan core.Event, 16)
		if _, err := p.runCommand(ctx, nil, agyRunOptions{
			prompt:         "hi",
			requestedModel: "gemini-4-neon",
			requestedEff:   "high",
			ephemeral:      true,
		}, eventSender{ctx: ctx, ch: events}); err != nil {
			t.Fatal(err)
		}
		close(events)

		for ev := range events {
			if ev.Type == core.EventContextUsage {
				t.Fatalf("ephemeral runCommand emitted EventContextUsage: %+v", ev.Context)
			}
		}
	})
}
