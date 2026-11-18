package ask

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
	"github.com/term-llm/term-llm/internal/session"
)

// Run the actual Ask writer. For restore cases seed a known-good row directly
// so a broken writer cannot mask an independent resume regression.
func TestAskReportedContextAcrossReopen(t *testing.T) {
	const oldUsed, oldWindow = 12000, 200000
	for _, tc := range []struct {
		name, provider, model, savedModel string
		resume                            bool
		live                              *core.ContextUsage
		wantUsed, wantWindow              int
	}{
		{"fresh_reading", "mock", "mock-model", "", false, &core.ContextUsage{UsedTokens: 34000, Window: 1000000}, 34000, 1000000},
		{"same_model_no_new_reading", "mock", "mock-model", "mock-model", true, nil, oldUsed, oldWindow},
		{"different_model_no_new_reading", "mock", "mock-model", "previous-model", true, nil, oldUsed, 0},
		{"different_model_new_known_window", "mock", "mock-model", "previous-model", true, &core.ContextUsage{UsedTokens: 850, Window: 1000000}, 850, 0},
		{"new_reading_unknown_window", "mock", "mock-model", "mock-model", true, &core.ContextUsage{UsedTokens: 320}, 320, 0},
		{"fresh_cli_default", "claude-bin", "", "", false, &core.ContextUsage{UsedTokens: 34000, Window: 1000000}, 34000, 1000000},
		{"same_cli_default_no_new_reading", "claude-bin", "", "", true, nil, oldUsed, oldWindow},
		{"explicit_model_differs_from_saved_cli_default", "claude-bin", "sonnet", "", true, nil, oldUsed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Chdir(t.TempDir())
			dir := filepath.Join(home, "term-llm")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			db := session.Config{Enabled: true, Path: filepath.Join(t.TempDir(), "sessions.db")}
			yaml := fmt.Sprintf("default_provider: %s\nproviders: {%s: {model: %q}}\nsessions: {enabled: true, path: %q}\nskills: {enabled: false}\n", tc.provider, tc.provider, tc.model, db.Path)
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0600); err != nil {
				t.Fatal(err)
			}
			opts := Options{Text: true, Porcelain: true, MaxTurns: 5}
			if tc.resume {
				store, err := session.NewSQLiteStore(db)
				if err != nil {
					t.Fatal(err)
				}
				meta := &session.Session{ID: session.NewID(), Mode: session.ModeAsk, Provider: "mock (" + tc.savedModel + ")", Model: tc.savedModel, LastTotalTokens: oldUsed, LastContextWindow: oldWindow}
				if tc.provider == "claude-bin" {
					meta.Provider, meta.ProviderKey = "Claude CLI (sonnet)", tc.provider
				}
				if err := store.Create(t.Context(), meta); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				opts.Resume = meta.ID
				opts.Changed.Resume = true
			}
			display := "mock (mock-model)"
			if tc.provider == "claude-bin" {
				display = "Claude CLI (sonnet)" // Also the real provider’s display name for a blank selector.
			}
			provider := mockprovider.NewMockProvider(display).WithCapabilities(core.Capabilities{ManagesOwnContext: true}).AddTurn(mockprovider.MockTurn{Text: "answer", Context: tc.live})
			var output bytes.Buffer
			deps := testDependencies(&output, &output)
			deps.SessionStore.DBPath = db.Path
			deps.NewProvider = func(*config.Config, bool) (core.Provider, error) { return provider, nil }
			if err := Run(t.Context(), []string{"question"}, opts, deps); err != nil {
				t.Fatalf("Ask: %v\n%s", err, &output)
			}
			store, err := session.NewSQLiteStore(db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			got, err := store.GetCurrent(t.Context())
			if err != nil || got == nil {
				t.Fatalf("reopen current Ask session: %v, %v", got, err)
			}
			if opts.Resume == "" && (got.ProviderKey != tc.provider || got.Provider != provider.Name() || got.Model != tc.model) {
				t.Errorf("new session provider key/display/model = %q/%q/%q", got.ProviderKey, got.Provider, got.Model)
			}
			if tc.provider == "claude-bin" {
				if reqs := provider.RecordedRequests(); len(reqs) != 1 || reqs[0].Model != tc.model {
					t.Fatalf("expected one request with model %q, got %+v", tc.model, reqs)
				}
			}
			if opts.Resume != "" && got.ID != opts.Resume {
				t.Fatalf("resumed %q, want %q", got.ID, opts.Resume)
			}
			if got.LastTotalTokens != tc.wantUsed || got.LastContextWindow != tc.wantWindow {
				t.Errorf("after SQLite reopen: used/window = %d/%d, want %d/%d", got.LastTotalTokens, got.LastContextWindow, tc.wantUsed, tc.wantWindow)
			}
		})
	}
}
