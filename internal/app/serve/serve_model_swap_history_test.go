package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
	"github.com/term-llm/term-llm/internal/session"
)

func TestConsecutiveModelSwapsPreserveTranscriptMarkers(t *testing.T) {
	ctx := context.Background()
	srv, store := newServeProjectTestServer(t)
	sess := &session.Session{ID: session.NewID(), Provider: "mock", ProviderKey: "mock", Model: "original", Mode: session.ModeChat, CompactionSeq: -1}
	if err := store.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	manager := newServeSessionManager(time.Minute, 10, nil)
	t.Cleanup(manager.Close)
	srv.sessionMgr = manager
	newRuntime := func(model string) (*serveRuntime, *mockprovider.MockProvider) {
		provider := mockprovider.NewMockProvider("mock").AddTextResponse("ready")
		rt := &serveRuntime{provider: provider, engine: engine.NewEngine(provider, nil), store: store, providerKey: "mock", defaultModel: model}
		rt.Touch()
		return rt, provider
	}
	previous, _ := newRuntime("original")
	putTestSession(manager, sess.ID, previous)

	from := "original"
	for i, target := range []string{"other", "original"} {
		candidate, provider := newRuntime(target)
		srv.runtimeFactory = func(context.Context, serveRuntimeRequest) (*serveRuntime, error) { return candidate, nil }
		plan := responseModelSwapPlan{enabled: true, previousProvider: "mock", previousModel: from, requestedProvider: "mock", requestedModel: target}
		input := []core.Message{core.UserText("continue")}
		exec, err := srv.beginResponseModelSwap(ctx, sess.ID, plan, input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(exec.markRolledBack)
		if _, _, err := srv.runResponseWithModelSwapFallback(ctx, candidate, true, false, input, core.Request{SessionID: sess.ID, Model: target}, sess.ID, exec); err != nil {
			t.Fatal(err)
		}

		stored, err := store.GetMessages(ctx, sess.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var markers []core.ModelSwapMarker
		for index, msg := range stored {
			if marker, ok := core.ParseModelSwapMarker(msg.ToLLMMessage()); ok {
				markers = append(markers, marker)
				if index == 0 || stored[index-1].Role != core.RoleUser || index+1 == len(stored) || stored[index+1].Role != core.RoleAssistant {
					t.Fatalf("marker is not between its triggering user message and reply: index %d", index)
				}
			}
		}
		if len(markers) != i+1 {
			t.Fatalf("after swap %d: got %d markers, want %d", i+1, len(markers), i+1)
		}
		if markers[0].FromModel != "original" || markers[0].ToModel != "other" {
			t.Fatalf("lost first transition: %#v", markers)
		}
		if markers[i].FromModel != from || markers[i].ToModel != target {
			t.Fatalf("incorrect latest transition: %#v", markers)
		}
		for _, req := range provider.Requests {
			for _, msg := range req.Messages {
				if msg.Role == core.RoleEvent {
					t.Fatal("transcript event leaked to provider")
				}
			}
		}
		from = target
	}
}

func TestModelSwapKeepsContextWindowWithItsModel(t *testing.T) {
	for _, path := range []string{"sync", "stream", "rollback", "storage error"} {
		t.Run(path, func(t *testing.T) { checkModelSwapContextWindow(t, path) })
	}
}

func checkModelSwapContextWindow(t *testing.T, path string) {
	t.Helper()
	ctx := t.Context()
	srv, store := newServeProjectTestServer(t)
	const providerKey = "claude-bin"
	source := core.ContextUsage{UsedTokens: 50_000, Window: 200_000}
	target := core.ContextUsage{UsedTokens: 634_800, Window: 1_000_000}
	meta := &session.Session{
		ID: session.NewID(), Provider: providerKey, ProviderKey: providerKey, Model: "sonnet",
		Mode: session.ModeChat, CompactionSeq: -1, LastTotalTokens: source.UsedTokens, LastContextWindow: source.Window,
	}
	if err := store.Create(ctx, meta); err != nil {
		t.Fatal(err)
	}
	manager := newServeSessionManager(time.Minute, 10, nil)
	t.Cleanup(manager.Close)
	srv.sessionMgr = manager
	makeRuntime := func(model string) (*serveRuntime, *mockprovider.MockProvider) {
		provider := mockprovider.NewMockProvider(providerKey).WithCapabilities(core.Capabilities{ManagesOwnContext: true})
		return &serveRuntime{provider: provider, engine: engine.NewEngine(provider, nil), store: store, providerKey: providerKey, defaultModel: model}, provider
	}
	previous, _ := makeRuntime("sonnet")
	putTestSession(manager, meta.ID, previous)
	candidate, provider := makeRuntime("opus")
	provider.AddTurn(mockprovider.MockTurn{Text: "reply", Context: &target})
	candidate.turnCompletedCB = func(ctx context.Context, _ int, _ []core.Message, _ core.TurnMetrics) error {
		// The candidate already reported tokens, but the committed row still
		// belongs to the source until the swap's outcome is known.
		got, err := store.Get(ctx, meta.ID)
		if err != nil || got == nil || got.Model != "sonnet" || got.LastTotalTokens != source.UsedTokens || got.LastContextWindow != source.Window {
			t.Errorf("before swap commit: got %+v, err %v; want source reading", got, err)
		}
		return nil
	}
	srv.runtimeFactory = func(context.Context, serveRuntimeRequest) (*serveRuntime, error) { return candidate, nil }
	if path == "storage error" {
		srv.store = rejectSwapContextStore{Store: store}
	}
	plan := responseModelSwapPlan{enabled: true, previousProvider: providerKey, previousModel: "sonnet", requestedProvider: providerKey, requestedModel: "opus"}
	input := []core.Message{core.UserText("continue")}
	exec, err := srv.beginResponseModelSwap(ctx, meta.ID, plan, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(exec.markRolledBack)
	req := core.Request{SessionID: meta.ID, Model: "opus"}
	wantModel, want := "opus", target
	switch path {
	case "stream":
		run, err := srv.startResponseRun(candidate, true, false, input, req, meta.ID, startResponseRunOptions{uiSession: true, modelSwap: exec})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.responseRuns.Close)
		select {
		case <-run.settled:
		case <-time.After(5 * time.Second):
			t.Fatal("swap did not settle")
		}
		if status := run.snapshot()["status"]; status != "completed" {
			t.Fatalf("swap status = %v", status)
		}
	case "rollback":
		// Exercise rollback after one target turn reported and accounted for
		// usage, as can happen before a later tool round fails.
		if _, err := candidate.Run(ctx, true, false, input, req); err != nil {
			t.Fatal(err)
		}
		exec.markRolledBack()
		srv.restoreModelSwapRollback(ctx, meta.ID, exec, candidate, "failed", "naive")
		wantModel, want = "sonnet", source
	case "storage error":
		if _, _, err := srv.runResponseWithModelSwapFallback(ctx, candidate, true, false, input, req, meta.ID, exec); !errors.Is(err, errSwapContextWrite) {
			t.Fatalf("swap returned %v instead of the storage error", err)
		}
		if active, ok := manager.Get(meta.ID); !ok || active != previous {
			t.Fatal("a failed storage commit dropped the original runtime")
		}
		wantModel, want = "sonnet", source
	default:
		if _, _, err := srv.runResponseWithModelSwapFallback(ctx, candidate, true, false, input, req, meta.ID, exec); err != nil {
			t.Fatal(err)
		}
	}
	// Read with no live runtime, as after server restart or idle eviction.
	cold := &serveServer{store: store}
	selected, err := cold.selectedWebSession(ctx, meta.ID, nil)
	if err != nil || selected == nil || selected.ContextUsage == nil {
		t.Fatalf("cold selected session = %+v, err %v", selected, err)
	}
	got := selected.ContextUsage
	if selected.Model != wantModel || got.UsedTokens != want.UsedTokens || got.InputLimit != want.Window {
		t.Fatalf("cold session %s: got %+v, want %s %+v", selected.Model, got, wantModel, want)
	}
}

var errSwapContextWrite = errors.New("swap context write failed")

type rejectSwapContextStore struct{ session.Store }

func (rejectSwapContextStore) UpdateWithContextEstimate(context.Context, *session.Session) error {
	return errSwapContextWrite
}
