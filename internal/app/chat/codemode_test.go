package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
	"github.com/term-llm/term-llm/internal/tools"
	tuichat "github.com/term-llm/term-llm/internal/tui/chat"
)

func TestCodeModeTerminalPromptsSerializeAndCancelWithTheirCells(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("not preapproved"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := tools.DefaultToolConfig()
	cfg.Enabled = []string{"spawn_script", "read_file"}
	cfg.BaseDir = dir
	cfg.Workspace = "none"
	manager, err := tools.NewToolManager(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.ApprovalMgr.Close)
	e := engine.NewEngine(mockprovider.NewMockProvider("terminal-cell"), nil)
	defer e.CloseCodeMode()
	manager.SetupEngine(e)
	messages := make(chan tea.Msg, 16)
	bindChatCodeModePrompts(manager, func(msg tea.Msg) { messages <- msg })
	const sessionID = "terminal"
	ctx, cancel := context.WithTimeout(core.ContextWithSessionID(t.Context(), sessionID), 20*time.Second)
	defer cancel()
	receive := func(want string) tea.Msg {
		t.Helper()
		select {
		case msg := <-messages:
			return msg
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v; cells: %+v", want, ctx.Err(), e.CodeModeCells(sessionID))
			return nil
		}
	}

	// The spawn receipt only acknowledges admission. Wait for the actual
	// terminal prompt, allowing for cold WASM compilation under -race.
	first, err := e.ExecCodeMode(ctx, `await tools.read_file({path:'file'});store('bad',1)`, 0, 0)
	if err != nil || first.IsError {
		t.Fatalf("first %+v %v", first, err)
	}
	msg := receive("first terminal prompt")
	request, ok := msg.(tuichat.ApprovalRequestMsg)
	if !ok {
		t.Fatalf("first terminal prompt: got %T", msg)
	}
	if request.Context == nil || !request.NoAutoApprove {
		t.Fatal("terminal prompt lost original cell authority/context")
	}

	second, err := e.ExecCodeMode(ctx, `await tools.read_file({path:'file'})`, 0, 0)
	if err != nil || second.IsError {
		t.Fatalf("second %+v %v", second, err)
	}
	// Wait until the second cell reaches approval while the first prompt is
	// still open. Script output before read_file would not prove this.
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
waiting:
	for {
		for _, cell := range e.CodeModeCells(sessionID) {
			if cell.CellID == second.CellID && cell.Status == "awaiting_approval" {
				break waiting
			}
		}
		select {
		case msg := <-messages:
			t.Fatalf("single-foreground terminal received concurrent prompt %T", msg)
		case <-ctx.Done():
			t.Fatalf("second cell did not reach approval: %+v", e.CodeModeCells(sessionID))
		case <-poll.C:
		}
	}

	// A later yolo toggle must not widen either admitted prompt-mode cell.
	manager.ApprovalMgr.SetApprovalMode(tools.ModeYolo)
	cancelled, err := e.CancelCodeMode(ctx, first.CellID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	msg = receive("first terminal prompt dismissal")
	if dismiss, ok := msg.(tuichat.DismissApprovalMsg); !ok || dismiss.DoneCh != request.DoneCh {
		t.Fatalf("expected first prompt to be dismissed before the next prompt, got %T", msg)
	}
	msg = receive("second terminal prompt")
	next, ok := msg.(tuichat.ApprovalRequestMsg)
	if !ok || next.DoneCh == request.DoneCh || next.Context == nil || !next.NoAutoApprove {
		t.Fatalf("second cell did not get its own prompt after the yolo toggle: %+v", msg)
	}

	e.InterruptCodeModeSession(sessionID)
	msg = receive("second terminal prompt dismissal")
	if dismiss, ok := msg.(tuichat.DismissApprovalMsg); !ok || dismiss.DoneCh != next.DoneCh {
		t.Fatalf("Stop did not dismiss second prompt, got %T", msg)
	}
	last, err := e.WaitCodeMode(ctx, second.CellID, 20*time.Second, 0)
	if err != nil || last.Status != "cancelled" {
		t.Fatalf("Stop %+v %v", last, err)
	}
	for e.HasAnyCodeModeCells() {
		select {
		case <-ctx.Done():
			t.Fatalf("terminal approval kept actual lease/admission place: %+v", e.CodeModeCells(sessionID))
		case <-poll.C:
		}
	}
}
