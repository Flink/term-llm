package chat

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/ui"
)

func reportedWindowSession() *session.Session {
	return &session.Session{ID: session.NewID(), Provider: "claude-bin", ProviderKey: "claude-bin", Model: "opus", Mode: session.ModeChat, CreatedAt: time.Now(), UpdatedAt: time.Now(), CompactionSeq: -1}
}

func reportedWindowChat(store session.Store, s *session.Session) (*Model, *mockprovider.MockProvider) {
	p := mockprovider.NewMockProvider("claude-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
	return newReportedWindowChat(store, s, p, engine.NewEngine(p, nil), "opus"), p
}

func newReportedWindowChat(store session.Store, s *session.Session, p *mockprovider.MockProvider, e *engine.Engine, model string) *Model {
	return New(&config.Config{DefaultProvider: "claude-bin"}, p, e, "claude-bin", model, nil, 20, false, false, false, nil, "", "", false, "", store, s, false, nil, false, false, "", "", false)
}

func streamWindowReading(t *testing.T, m *Model, p *mockprovider.MockProvider, reading core.ContextUsage) {
	t.Helper()
	p.AddTurn(mockprovider.MockTurn{Text: "done", Context: &reading})
	stream, err := m.engine.Stream(t.Context(), core.Request{Messages: []core.Message{core.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if got := m.engine.ContextUsage(); got != reading {
		t.Fatalf("live reading = %+v, want %+v", got, reading)
	}
}

func TestTUIContextWindowStreamDoneSQLiteReopen(t *testing.T) {
	for _, tc := range []struct {
		name, savedProvider, savedModel, activeModel string
		wantWindow                                   int
	}{
		{"matching identity", "claude-bin", "opus", "opus", 1000000},
		{"CLI default", "claude-bin", "", "", 1000000},
		{"different provider", "other-provider", "opus", "opus", 0},
		{"different model", "claude-bin", "sonnet", "opus", 0},
		{"CLI default differs from saved model", "claude-bin", "sonnet", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := session.Config{Enabled: true, Path: filepath.Join(t.TempDir(), "sessions.db")}
			store, err := session.NewSQLiteStore(cfg)
			if err != nil {
				t.Fatal(err)
			}
			s := reportedWindowSession()
			s.ProviderKey, s.Model = tc.savedProvider, tc.savedModel
			if err := store.Create(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			p := mockprovider.NewMockProvider("claude-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
			m := newReportedWindowChat(store, s, p, engine.NewEngine(p, nil), tc.activeModel)
			reading := core.ContextUsage{UsedTokens: 634800, Window: 1000000}
			streamWindowReading(t, m, p, reading)
			m.streaming = true
			m.Update(streamEventMsg{event: ui.DoneEvent(42)})
			if m.sess.LastTotalTokens != reading.UsedTokens || m.sess.LastContextWindow != tc.wantWindow {
				t.Errorf("stream done: in-memory estimate = %d/%d, want %d/%d", m.sess.LastTotalTokens, m.sess.LastContextWindow, reading.UsedTokens, tc.wantWindow)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := session.NewSQLiteStore(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			saved, err := reopened.Get(t.Context(), s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.LastTotalTokens != reading.UsedTokens || saved.LastContextWindow != tc.wantWindow {
				t.Errorf("reopened SQLite estimate = %d/%d, want %d/%d", saved.LastTotalTokens, saved.LastContextWindow, reading.UsedTokens, tc.wantWindow)
			}
		})
	}
}

func TestTUIContextWindowRestore(t *testing.T) {
	want := core.ContextUsage{UsedTokens: 12000, Window: 200000}
	saved := reportedWindowSession()
	saved.LastTotalTokens, saved.LastContextWindow = want.UsedTokens, want.Window
	t.Run("cold resume", func(t *testing.T) {
		m, _ := reportedWindowChat(nil, saved)
		if got := m.engine.ContextUsage(); got != want {
			t.Fatalf("restored = %+v, want %+v", got, want)
		}
	})
	t.Run("switch sessions on same model", func(t *testing.T) {
		m, p := reportedWindowChat(nil, reportedWindowSession())
		streamWindowReading(t, m, p, core.ContextUsage{UsedTokens: 4000, Window: 1000000})
		m.Update(sessionLoadedMsg{sess: saved})
		if got := m.engine.ContextUsage(); got != want {
			t.Fatalf("selected session = %+v, want %+v", got, want)
		}
	})
	t.Run("stale row cannot replace newer unknown window", func(t *testing.T) {
		m, p := reportedWindowChat(nil, saved)
		live := core.ContextUsage{UsedTokens: 6000}
		streamWindowReading(t, m, p, live)
		m.configureContextManagementForSession()
		if got := m.engine.ContextUsage(); got != live {
			t.Fatalf("after reconfiguration = %+v, want live %+v", got, live)
		}
	})
}

func TestTUIContextWindowRestoreUsesActiveIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, savedProvider, savedModel, activeModel string
		wantWindow                                   int
	}{
		{"different provider", "other-provider", "opus", "opus", 0},
		{"different model", "claude-bin", "other-model", "opus", 0},
		{"same CLI default", "claude-bin", "", "", 200000},
		{"CLI default differs from saved model", "claude-bin", "sonnet", "", 0},
		{"explicit model differs from saved CLI default", "claude-bin", "", "sonnet", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := reportedWindowSession()
			saved.ProviderKey, saved.Model = tc.savedProvider, tc.savedModel
			saved.LastTotalTokens, saved.LastContextWindow = 12000, 200000
			p := mockprovider.NewMockProvider("claude-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
			m := newReportedWindowChat(nil, saved, p, engine.NewEngine(p, nil), tc.activeModel)
			if got := m.engine.ContextUsage(); got.Window != tc.wantWindow {
				t.Fatalf("active runtime context = %+v, want window %d", got, tc.wantWindow)
			}
		})
	}
}

func TestTUIContextWindowClearedOnSessionRelabel(t *testing.T) {
	for _, kind := range []string{"model", "effort"} {
		t.Run(kind, func(t *testing.T) {
			saved := reportedWindowSession()
			saved.LastTotalTokens, saved.LastContextWindow = 12000, 200000
			m, _ := reportedWindowChat(nil, saved)
			switch kind {
			case "model":
				m.switchModel("debug:fast")
			case "effort":
				m.switchEffortStateOnly(effortSwitchResolution{provider: "claude-bin", targetModel: "opus-high", label: "high", ok: true}, false)
			}
			if m.sess.Model == "opus" {
				t.Fatal("session did not switch models")
			}
			if m.sess.LastContextWindow != 0 {
				t.Fatalf("relabeled metadata kept window %d", m.sess.LastContextWindow)
			}
			m.configureContextManagementForSession()
			if _, _, window := m.engine.ContextEstimateSnapshot(); window != 0 {
				t.Fatalf("configured new model with old reported window %d", window)
			}
		})
	}
}

// The TUI may be given an engine that has already observed the active turn.
// An older session row (including one with no estimate) must not clear it.
func TestTUIContextWindowNewPreservesLiveEngine(t *testing.T) {
	for _, used := range []int{0, 12000} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			saved := reportedWindowSession()
			saved.LastTotalTokens = used
			if used > 0 {
				saved.LastContextWindow = 200000
			}
			first, p := reportedWindowChat(nil, saved)
			live := core.ContextUsage{UsedTokens: 6000}
			streamWindowReading(t, first, p, live)
			resumed := newReportedWindowChat(nil, saved, p, first.engine, "opus")
			if got := resumed.engine.ContextUsage(); got != live {
				t.Fatalf("New replaced live reading %+v with %+v", live, got)
			}
		})
	}
}

func TestTUIStatusLineAndStatsShowReportedContextUsage(t *testing.T) {
	saved := reportedWindowSession()
	saved.ProviderKey, saved.Model = "agy-bin", "gemini-4-neon"
	p := mockprovider.NewMockProvider("agy-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
	m := New(&config.Config{DefaultProvider: "agy-bin"}, p, engine.NewEngine(p, nil), "agy-bin", "gemini-4-neon", nil, 20, false, false, false, nil, "", "", false, "", nil, saved, false, nil, false, false, "", "", false)
	m.width = 120

	// Live reading with known window appears in both status line and /stats,
	// including while streaming.
	streamWindowReading(t, m, p, core.ContextUsage{UsedTokens: 164_500, Window: 1_048_576})

	m.streaming = true
	streamingStatus := ui.StripANSI(m.renderStatusLine())
	if !strings.Contains(streamingStatus, "~165K/1M") {
		t.Fatalf("streaming status line %q missing reported context ~165K/1M", streamingStatus)
	}

	m.streaming = false
	idleStatus := ui.StripANSI(m.renderStatusLine())
	if !strings.Contains(idleStatus, "~165K/1M") {
		t.Fatalf("idle status line %q missing reported context ~165K/1M", idleStatus)
	}

	modal := m.renderStatsModal()
	if !strings.Contains(modal, "164.5k/1.05M tokens") {
		t.Fatalf("stats modal did not show reported context/window:\n%s", modal)
	}
	if strings.Contains(modal, "context window unknown") {
		t.Fatalf("stats modal unexpectedly claimed unknown window:\n%s", modal)
	}

	// Reading with unknown window shows token count without guessing a window.
	streamWindowReading(t, m, p, core.ContextUsage{UsedTokens: 42_000})
	unknownStatus := ui.StripANSI(m.renderStatusLine())
	if !strings.Contains(unknownStatus, "~42K") || strings.Contains(unknownStatus, "~42K/") {
		t.Fatalf("unknown-window status line %q should show ~42K without a limit", unknownStatus)
	}
	unknownModal := m.renderStatsModal()
	if !strings.Contains(unknownModal, "context window unknown") || !strings.Contains(unknownModal, "42k tokens used") {
		t.Fatalf("unknown-window stats modal unexpected:\n%s", unknownModal)
	}
}

func TestTUIStatusLineAndStatsShowRestoredAgyBinContextWindow(t *testing.T) {
	saved := reportedWindowSession()
	saved.Provider, saved.ProviderKey, saved.Model = "agy-bin", "agy-bin", "gemini-4-neon"
	saved.LastTotalTokens, saved.LastContextWindow = 96_400, 1_048_576
	p := mockprovider.NewMockProvider("agy-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
	m := New(&config.Config{DefaultProvider: "agy-bin"}, p, engine.NewEngine(p, nil), "agy-bin", "gemini-4-neon", nil, 20, false, false, false, nil, "", "", false, "", nil, saved, false, nil, false, false, "", "", false)
	m.width = 120

	idleStatus := ui.StripANSI(m.renderStatusLine())
	if !strings.Contains(idleStatus, "~96K/1M") {
		t.Fatalf("restored idle status line %q missing ~96K/1M", idleStatus)
	}
	m.streaming = true
	streamingStatus := ui.StripANSI(m.renderStatusLine())
	if !strings.Contains(streamingStatus, "~96K/1M") {
		t.Fatalf("restored streaming status line %q missing ~96K/1M", streamingStatus)
	}
	modal := m.renderStatsModal()
	if !strings.Contains(modal, "96.4k/1.05M tokens") {
		t.Fatalf("restored stats modal missing 96.4k/1.05M tokens:\n%s", modal)
	}
}

func TestTUIStatusLineHidesStaleTokensForNonManagedUnknownLimitProvider(t *testing.T) {
	p := mockprovider.NewMockProvider("mock")
	eng := engine.NewEngine(p, nil)
	eng.SetContextEstimateBaseline(50_000, 2)
	m := New(&config.Config{DefaultProvider: "mock"}, p, eng, "mock", "unknown-model", nil, 20, false, false, false, nil, "", "", false, "", nil, nil, false, nil, false, false, "", "", false)
	m.width = 120

	status := ui.StripANSI(m.renderStatusLine())
	if strings.Contains(status, "~50K") {
		t.Fatalf("non-managed provider with unknown limit leaked stale baseline in status line: %q", status)
	}
}

func TestTUIStatusLineHidesStaleTokensForManagedProviderResumingDifferentProvider(t *testing.T) {
	saved := reportedWindowSession()
	saved.Provider, saved.ProviderKey, saved.Model = "Anthropic", "anthropic", "claude-sonnet-4-6"
	saved.LastTotalTokens, saved.LastContextWindow = 50_000, 200_000

	p := mockprovider.NewMockProvider("grok-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
	m := New(&config.Config{DefaultProvider: "grok-bin"}, p, engine.NewEngine(p, nil), "grok-bin", "grok-4.6", nil, 20, false, false, false, nil, "", "", false, "", nil, saved, false, nil, false, false, "", "", false)
	m.width = 120

	if got := m.engine.ContextUsage(); got != (core.ContextUsage{}) {
		t.Fatalf("managed provider resuming different provider inherited context = %+v, want empty", got)
	}
	status := ui.StripANSI(m.renderStatusLine())
	if strings.Contains(status, "~50K") {
		t.Fatalf("managed provider resuming different provider leaked stale baseline in status line: %q", status)
	}
}
