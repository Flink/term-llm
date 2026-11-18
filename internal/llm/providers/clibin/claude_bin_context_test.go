package clibin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func claudeAssistantUsageLine(id, parent string, input, cacheRead, cacheWrite int) string {
	parentField := `null`
	if parent != "" {
		parentField = `"` + parent + `"`
	}
	return `{"type":"assistant","parent_tool_use_id":` + parentField + `,"message":{"id":"` + id +
		`","model":"claude-opus-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":` + strconv.Itoa(input) +
		`,"cache_read_input_tokens":` + strconv.Itoa(cacheRead) + `,"cache_creation_input_tokens":` + strconv.Itoa(cacheWrite) +
		`,"output_tokens":16}}}`
}

// Shapes mirror a real Claude Code 2.1.303 stream-json tool loop: per-call
// usage on each assistant event, and a result whose usage is summed across all
// calls while modelUsage carries the context window.
func TestClaudeBinReportsContextPerAPICall(t *testing.T) {
	p := NewClaudeBinProvider("", nil)
	events := make(chan core.Event, 32)
	send := eventSender{ctx: context.Background(), ch: events}
	var (
		lastUsage       *core.Usage
		sawTextDelta    bool
		fallbackText    string
		handledTerminal bool
	)
	lines := []string{
		claudeAssistantUsageLine("msg_1", "", 2, 12597, 12915),
		// Same API call repeated for a second content block: no new reading.
		claudeAssistantUsageLine("msg_1", "", 2, 12597, 12915),
		// A subagent call runs in its own window and must not move the meter.
		claudeAssistantUsageLine("msg_side", "toolu_1", 2, 900000, 0),
		claudeAssistantUsageLine("msg_2", "", 2, 25512, 78),
		claudeAssistantUsageLine("msg_3", "", 2, 25590, 78),
		`{"type":"result","subtype":"success","is_error":false,"result":"done","usage":{"input_tokens":6,"cache_creation_input_tokens":13071,"cache_read_input_tokens":63699,"output_tokens":184},"modelUsage":{"claude-opus-5-5":{"inputTokens":6,"contextWindow":1000000,"maxOutputTokens":128000}}}`,
	}
	for _, line := range lines {
		if err := p.handleClaudeLine(context.Background(), line, false, send, &lastUsage, &sawTextDelta, &fallbackText, &handledTerminal, nil, false); err != nil {
			t.Fatalf("handleClaudeLine: %v", err)
		}
	}
	close(events)

	var got []core.ContextUsage
	for ev := range events {
		if ev.Type == core.EventContextUsage {
			got = append(got, *ev.Context)
		}
	}
	want := []core.ContextUsage{
		{UsedTokens: 25514},
		{UsedTokens: 25592},
		{UsedTokens: 25670},
		{UsedTokens: 25670, Window: 1000000},
	}
	if len(got) != len(want) {
		t.Fatalf("context readings = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("context reading %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The window is known from the first turn onwards, so later turns report
	// it with every mid-turn checkpoint.
	events = make(chan core.Event, 4)
	send = eventSender{ctx: context.Background(), ch: events}
	if err := p.handleClaudeLine(context.Background(), claudeAssistantUsageLine("msg_4", "", 3, 25700, 200), false, send, &lastUsage, &sawTextDelta, &fallbackText, &handledTerminal, nil, false); err != nil {
		t.Fatalf("handleClaudeLine: %v", err)
	}
	close(events)
	ev := <-events
	if ev.Type != core.EventContextUsage || *ev.Context != (core.ContextUsage{UsedTokens: 25903, Window: 1000000}) {
		t.Fatalf("next turn reading = %+v, want 25903/1000000", ev.Context)
	}

	// A mid-run model change (e.g. Claude Code's fallback model) invalidates
	// the window until that model's result reports its own.
	events = make(chan core.Event, 4)
	send = eventSender{ctx: context.Background(), ch: events}
	sonnet := strings.Replace(claudeAssistantUsageLine("msg_5", "", 3, 26000, 0), "claude-opus-5-5", "claude-sonnet-5", 1)
	if err := p.handleClaudeLine(context.Background(), sonnet, false, send, &lastUsage, &sawTextDelta, &fallbackText, &handledTerminal, nil, false); err != nil {
		t.Fatalf("handleClaudeLine: %v", err)
	}
	close(events)
	ev = <-events
	if ev.Type != core.EventContextUsage || *ev.Context != (core.ContextUsage{UsedTokens: 26003}) {
		t.Fatalf("model-change reading = %+v, want 26003 with unknown window", ev.Context)
	}
}

func TestClaudeBinEphemeralRequestsDoNotReportContext(t *testing.T) {
	p := NewClaudeBinProvider("", nil)
	events := make(chan core.Event, 4)
	send := eventSender{ctx: context.Background(), ch: events}
	var (
		lastUsage       *core.Usage
		sawTextDelta    bool
		fallbackText    string
		handledTerminal bool
	)
	if err := p.handleClaudeLine(context.Background(), claudeAssistantUsageLine("msg_1", "", 2, 100, 100), false, send, &lastUsage, &sawTextDelta, &fallbackText, &handledTerminal, nil, true); err != nil {
		t.Fatalf("handleClaudeLine: %v", err)
	}
	close(events)
	for ev := range events {
		if ev.Type == core.EventContextUsage {
			t.Fatalf("ephemeral request reported context %+v", ev.Context)
		}
	}
}

func TestClaudeResultWindowMatchesAssistantModel(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, tt := range []struct {
		name, usage string
		want        int
	}{
		{"exact before alias", `"claude-sonnet-4-6":{"contextWindow":200000},"sonnet[1m]":{"canonicalModel":"claude-sonnet-4-6","contextWindow":1000000}`, 200000},
		{"extended selector", `"claude-sonnet-4-6[1m]":{"contextWindow":1000000},"haiku":{"contextWindow":200000}`, 1000000},
		{"reported alias", `"sonnet[1m]":{"canonicalModel":"claude-sonnet-4-6","contextWindow":500000},"haiku":{"contextWindow":200000}`, 500000},
		{"reported canonical suffix", `"sonnet[1m]":{"canonicalModel":"claude-sonnet-4-6[1m]","contextWindow":1000000}`, 1000000},
		{"ambiguous aliases", `"sonnet":{"canonicalModel":"claude-sonnet-4-6","contextWindow":200000},"sonnet[1m]":{"canonicalModel":"claude-sonnet-4-6","contextWindow":1000000}`, 0},
		{"unrelated singleton", `"haiku":{"contextWindow":200000}`, 0},
		{"unresolved alias", `"sonnet[1m]":{"contextWindow":1000000}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := claudeContextReport{model: model, sawModel: true, used: 123}
			got := r.observe("result", `{"type":"result","modelUsage":{`+tt.usage+`}}`)
			if tt.want == 0 {
				if got != nil {
					t.Fatalf("unexpected reading %+v", got)
				}
				return
			}
			if got == nil || *got != (core.ContextUsage{UsedTokens: 123, Window: tt.want}) {
				t.Fatalf("reading = %+v, want 123/%d", got, tt.want)
			}
		})
	}
}

func TestClaudeContextFallbackWithoutUsageDoesNotRelabelPriorReading(t *testing.T) {
	report := claudeContextReport{selector: "opus"}
	report.observe("assistant", claudeAssistantUsageLine("msg_opus", "", 123, 0, 0))
	report.observe("assistant", `{"type":"assistant","message":{"id":"msg_sonnet","model":"claude-sonnet-5-5"}}`)
	if got := report.observe("result", `{"type":"result","modelUsage":{"claude-sonnet-5-5":{"contextWindow":200000}}}`); got != nil {
		t.Fatalf("fallback result relabeled the previous model's occupancy: %+v", got)
	}
	report.beginTurn()
	got := report.observe("assistant", `{"type":"assistant","message":{"id":"msg_new","model":"claude-sonnet-5-5","usage":{"input_tokens":456}}}`)
	if got == nil || *got != (core.ContextUsage{UsedTokens: 456, Window: 200000}) {
		t.Fatalf("next turn did not retain the fallback model's verified window: %+v", got)
	}
}

func TestClaudeContextPassiveSubprocess(t *testing.T) {
	const script = `#!/usr/bin/env python3
import json, sys

opus = 'claude-opus-5-5'
sonnet = 'claude-sonnet-5-5'

def emit(**kwargs):
    print(json.dumps(kwargs), flush=True)

def assistant(model=None, tokens=None, id='main', parent=None):
    msg = {'id': id}
    if model is not None:
        msg['model'] = model
    if tokens is not None:
        msg['usage'] = {'input_tokens': tokens}
    emit(type='assistant', message=msg, parent_tool_use_id=parent)

user = json.loads(sys.stdin.readline())
assert user['type'] == 'user'
mode = user['message']['content'][0]['text']
emit(type='system', subtype='init', session_id='test-session', model='unverified-init-model')
usage = {}
if mode == 'learn':
    assistant(opus, 123)
    assistant(tokens=999, id='unknown') # Uncorrelated partial cannot relabel usage.
    usage = {'opus[1m]': {'canonicalModel': opus, 'contextWindow': 1000000}}
elif mode == 'same' or mode == 'extended':
    if mode == 'extended':
        assert sys.argv[sys.argv.index('--model') + 1] == 'opus[1m]'
        assert sys.argv[sys.argv.index('--resume') + 1] == 'test-session'
    assistant(opus, 123) # Repeat the message id and usage to exercise per-turn reset.
    usage = {'haiku': {'contextWindow': 200000}}
elif mode == 'result-only':
    usage = {opus: {'contextWindow': 77000}}
elif mode == 'no-real-assistant':
    assistant(tokens=9) # The id from a past turn does not identify this frame.
    assistant('<synthetic>', 10)
    assistant(opus, 11, parent='toolu_side')
    usage = {opus: {'contextWindow': 88000}}
elif mode == 'ephemeral':
    assistant(sonnet, 321)
    usage = {sonnet: {'contextWindow': 204800}}
elif mode == 'fallback':
    assistant(opus, 123)
    assistant(sonnet, id='fallback')
    assistant(tokens=456, id='fallback') # Model-less partial of an identified message.
    usage = {opus: {'contextWindow': 1000000},
             'sonnet': {'canonicalModel': sonnet, 'contextWindow': 200000},
             'sonnet[1m]': {'canonicalModel': sonnet, 'contextWindow': 1000000}}
elif mode == 'different':
    assistant(sonnet, 456)
    usage = {opus: {'contextWindow': 1000000}}
else:
    raise AssertionError(mode)
emit(type='result', subtype='success', result='done', modelUsage=usage)
# EOF arrives only after the result; there must never have been another input frame.
assert not sys.stdin.read()
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	run := func(t *testing.T, p *ClaudeBinProvider, messages []core.Message, model, action string, want ...core.ContextUsage) []core.Message {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ephemeral := action == "ephemeral"
		if ephemeral {
			messages = nil
		}
		messages = append(messages, core.UserText(action))
		stream, err := p.Stream(ctx, core.Request{Model: model, Messages: messages, Ephemeral: ephemeral})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var got []core.ContextUsage
		for {
			ev, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err == nil {
				err = ev.Err
			}
			if err != nil {
				var ce *CLICommandError
				if errors.As(err, &ce) {
					t.Fatalf("fake CLI (%s) failed: %s", action, ce.StderrTail)
				}
				t.Fatal(err)
			}
			if ev.Type == core.EventContextUsage {
				got = append(got, *ev.Context)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s readings = %+v, want %+v", action, got, want)
		}
		return append(messages, core.AssistantText("done"))
	}

	p := NewClaudeBinProvider("opus", nil)
	known := core.ContextUsage{UsedTokens: 123, Window: 1000000}
	unknown := core.ContextUsage{UsedTokens: 123}
	transcript := run(t, p, nil, "", "learn", unknown, known)
	state, ok := p.ExportProviderState()
	if !ok {
		t.Fatal("expected provider state after learning the window")
	}
	var exported map[string]any
	if err := json.Unmarshal(state, &exported); err != nil {
		t.Fatal(err)
	}
	if exported["context_model"] != "claude-opus-5-5" || exported["context_window"] != float64(1000000) || exported["context_selector"] != "opus" {
		t.Fatalf("unexpected persisted window: %s", state)
	}
	// The blob has only its usual three continuation fields and the three
	// context fields: no per-turn usage or message identity is carried over.
	if len(exported) != 6 {
		t.Fatalf("unexpected fields in provider state: %s", state)
	}

	t.Run("CLI default is measured again", func(t *testing.T) {
		p := NewClaudeBinProvider("", nil)
		messages := run(t, p, nil, "", "learn", unknown, known)
		state, _ := p.ExportProviderState()
		if strings.Contains(string(state), "context_window") {
			t.Fatalf("unresolved selector should not checkpoint a reusable window: %s", state)
		}
		run(t, p, messages, "", "same", unknown)
	})

	t.Run("subsequent subprocesses", func(t *testing.T) {
		messages := run(t, p, transcript, "", "same", known)
		messages = run(t, p, messages, "", "result-only")
		messages = run(t, p, messages, "", "no-real-assistant")
		run(t, p, nil, "sonnet", "ephemeral")
		messages = run(t, p, messages, "", "same", known)
		messages = run(t, p, messages, "", "fallback", known, core.ContextUsage{UsedTokens: 456})
		run(t, p, messages, "", "same", unknown)
	})

	for _, tc := range []struct {
		name, model, action string
		want                []core.ContextUsage
	}{
		{"same effective selector and actual model", "opus-max", "same", []core.ContextUsage{known}},
		{"same selector but different actual model", "opus", "different", []core.ContextUsage{{UsedTokens: 456}}},
		{"different selector with same wire model", "opus[1m]", "extended", []core.ContextUsage{unknown}},
		{"no confirmed main model", "opus", "no-real-assistant", nil},
	} {
		t.Run("rehydrate/"+tc.name, func(t *testing.T) {
			restored := NewClaudeBinProvider("opus", nil)
			if err := restored.ImportProviderState(state); err != nil {
				t.Fatal(err)
			}
			run(t, restored, slices.Clone(transcript), tc.model, tc.action, tc.want...)
		})
	}
}

func TestClaudeContextProviderStateValidationAndReset(t *testing.T) {
	for _, fields := range []string{
		``, // Legacy state.
		`,"context_model":"claude-opus-5-5","context_window":0,"context_selector":"opus"`,
		`,"context_model":"claude-opus-5-5","context_window":-1,"context_selector":"opus"`,
		`,"context_model":"<synthetic>","context_window":1000000,"context_selector":"opus"`,
		`,"context_model":" ","context_window":1000000,"context_selector":"opus"`,
		`,"context_window":1000000,"context_selector":"opus"`,
		`,"context_model":"claude-opus-5-5","context_window":1000000`,
		`,"context_model":"claude-opus-5-5","context_window":1000000,"context_selector":" "`,
	} {
		t.Run(fields, func(t *testing.T) {
			p := NewClaudeBinProvider("opus", nil)
			if err := p.ImportProviderState([]byte(`{"session_id":"native","messages_sent":2` + fields + `}`)); err != nil {
				t.Fatal(err)
			}
			state, ok := p.ExportProviderState()
			if !ok || strings.Contains(string(state), "context_") || p.sessionID != "native" || p.messagesSent != 2 {
				t.Fatalf("invalid window affected legacy continuation: %s", state)
			}
		})
	}

	p := NewClaudeBinProvider("opus", nil)
	state := []byte(`{"session_id":"native","messages_sent":2,"context_model":"claude-opus-5-5","context_window":1000000,"context_selector":"opus"}`)
	if err := p.ImportProviderState(state); err != nil {
		t.Fatal(err)
	}
	p.ResetConversation()
	p.contextReport.useSelector("opus")
	got := p.contextReport.observe("assistant", claudeAssistantUsageLine("msg_new", "", 123, 0, 0))
	if got == nil || *got != (core.ContextUsage{UsedTokens: 123}) {
		t.Fatalf("fresh conversation inherited a stale window: %+v", got)
	}
}
