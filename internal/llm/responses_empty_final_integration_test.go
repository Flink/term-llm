package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/credentials"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/providers/responsesproviders"
)

func TestResponsesEmptyFinalAfterTool(t *testing.T) {
	const final = `{"type":"message","id":"msg_final","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":""}]}`
	const reasoning = `{"type":"reasoning","id":"rs_final","encrypted_content":"opaque-fixture","summary":[]}`
	for _, tc := range []struct {
		name              string
		finalItems        []string
		terminalOnlyFinal bool
	}{
		{"empty_final", []string{final}, false},
		{"opaque_reasoning_then_empty_final", []string{reasoning, final}, false},
		{"empty_final_only_in_terminal_snapshot", []string{reasoning, final}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &countingTool{}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				items := tc.finalItems
				if n == 1 {
					items = []string{fmt.Sprintf(`{"type":"function_call","call_id":"call_test","name":%q,"arguments":"{}"}`, tool.Spec().Name)}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for i, item := range items {
					if tc.terminalOnlyFinal && item == final {
						continue
					}
					fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", i, item)
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"status\":\"completed\",\"usage\":{\"input_tokens\":20,\"output_tokens\":4},\"output\":[%s]}}\n\n", n, strings.Join(items, ","))
			}))
			defer server.Close()

			provider := responsesproviders.NewChatGPTProviderWithCredsAndOptions(&credentials.ChatGPTCredentials{
				AccessToken: "fixture", AccountID: "fixture", ExpiresAt: time.Now().Add(time.Hour).Unix(),
			}, "gpt-test", responsesproviders.ChatGPTProviderOptions{BaseURL: server.URL})
			defer provider.ResetConversation()
			registry := engine.NewToolRegistry()
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
			e := engine.NewEngine(provider, registry)
			var replay []string
			var finalCallbacks int
			e.SetTurnCompletedCallback(func(_ context.Context, turn int, msgs []core.Message, metrics core.TurnMetrics) error {
				if turn == 1 {
					finalCallbacks++
					if len(msgs) != 1 || core.MessageText(msgs[0]) != "" || metrics.InputTokens != 20 || metrics.OutputTokens != 4 {
						t.Errorf("final messages = %+v, metrics = %+v", msgs, metrics)
					}
					for _, msg := range msgs {
						for _, part := range msg.Parts {
							if part.ProviderReplay != nil {
								replay = append(replay, string(part.ProviderReplay.Raw))
							}
						}
					}
				}
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := e.Stream(ctx, core.Request{
				Messages: []core.Message{core.UserText("Run the tool; no reply is needed.")},
				Model:    "gpt-test", MaxTurns: 4, Tools: []core.ToolSpec{tool.Spec()},
				ToolChoice: core.ToolChoice{Mode: core.ToolChoiceAuto},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			drainStream(t, stream)
			if finalCallbacks != 1 || !slices.Equal(replay, tc.finalItems) {
				t.Errorf("final callbacks = %d, persisted replay = %q", finalCallbacks, replay)
			}
			if n, calls := requests.Load(), tool.calls.Load(); n != 2 || calls != 1 {
				t.Fatalf("requests = %d (want 2), tool invocations = %d (want 1)", n, calls)
			}
		})
	}
}
