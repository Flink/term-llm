package tools

import (
	"context"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestObserveAgentStatusDoesNotCollectDetachedChild(t *testing.T) {
	r := &lifecycleRunner{entered: make(chan string, 1), release: make(chan struct{})}
	spawn := NewSpawnAgentTool(DefaultSpawnConfig(), 0)
	spawn.SetRunner(r)
	parent := "observe-parent"
	ctx := core.ContextWithSessionID(t.Context(), parent)
	child := lifecycleResult(t, lifecycleCall(t, spawn, ctx, `{"agent_name":"developer","prompt":"work","wait":0}`)).AgentID
	<-r.entered
	defer processAgentEntries.Delete(child)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ObserveAgentStatus(cancelled, nil, parent, child); err == nil {
		t.Fatal("cancelled observer should stop, not stop the child")
	}
	if _, err := ObserveAgentStatus(ctx, nil, "different-parent", child); err == nil {
		t.Fatal("observed another parent's child")
	}
	go close(r.release)
	status, err := ObserveAgentStatus(ctx, nil, parent, child)
	if status != "completed" || err != nil {
		t.Fatalf("observed %q, %v", status, err)
	}
	record, _, err := spawn.manager.get(ctx, child, parent)
	if err != nil || !record.CollectedAt.IsZero() {
		t.Fatalf("observation collected result: %v, %v", record.CollectedAt, err)
	}
}
