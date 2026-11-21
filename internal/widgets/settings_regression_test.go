package widgets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBadReloadKeepsAppliedDisabledIDsUntilSettingsAreRemoved(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"alpha", "beta", "gamma", "delta"} {
		writeWidget(t, dir, id, "title: Widget\ncommand: [echo, $PORT]\n")
	}
	writeSettingsFile(t, dir, "disabled: [alpha, gamma, not-yet-installed]\n")
	m := NewManager(dir, "/ui")
	defer m.Close()

	// External edits to unrelated IDs still require reload, even if a toggle
	// merges them into the rewritten file.
	writeSettingsFile(t, dir, "disabled: [alpha, gamma, not-yet-installed, delta]\n")
	if err := m.SetDisabled("beta", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDisabled("gamma", false); err != nil {
		t.Fatal(err)
	}
	writeSettingsFile(t, dir, "disabled: [\n")
	writeWidget(t, dir, "alpha", "title: Alpha\nmount: renamed\ncommand: [echo, $PORT]\n")
	writeWidget(t, dir, "not-yet-installed", "title: New Widget\ncommand: [echo, $PORT]\n")
	if errs := m.Reload(); len(errs) != 1 || !strings.Contains(errs[0].Error(), SettingsFile) {
		t.Fatalf("bad reload errors = %v, want one naming %s", errs, SettingsFile)
	}
	for mount, want := range map[string]string{
		"renamed": "disabled", "beta": "disabled", "gamma": "stopped",
		"not-yet-installed": "disabled", "delta": "stopped",
	} {
		if got := widgetState(t, m, mount); got != want {
			t.Errorf("%s after bad reload = %q, want %q", mount, got, want)
		}
	}

	if err := os.Remove(filepath.Join(dir, SettingsFile)); err != nil {
		t.Fatal(err)
	}
	if errs := m.Reload(); len(errs) != 0 {
		t.Fatalf("missing file reload: %v", errs)
	}
	writeSettingsFile(t, dir, "disabled: [\n")
	m.Reload() // No stale IDs should come back on a subsequent bad reload.
	for _, status := range m.Status() {
		if status.State != "stopped" {
			t.Errorf("missing file did not reset preferences: %+v", status)
		}
	}
}

func TestTrailingSettingsDocumentsAreRejectedAndNotOverwritten(t *testing.T) {
	for name, doc := range map[string]string{
		"valid": "---\ndisabled: [other]\n", "empty": "---\n", "invalid": "---\ndisabled: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeWidget(t, dir, "alpha", "title: Alpha\ncommand: [echo, $PORT]\n")
			original := "disabled: []\n" + doc
			writeSettingsFile(t, dir, original)
			m := NewManager(dir, "/ui")
			defer m.Close()
			if errs := m.LoadErrors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), SettingsFile) {
				t.Errorf("load errors = %v, want one naming %s", errs, SettingsFile)
			}
			if err := m.SetDisabled("alpha", true); err == nil {
				t.Error("SetDisabled accepted trailing document")
			}
			if data, err := os.ReadFile(filepath.Join(dir, SettingsFile)); err != nil || string(data) != original {
				t.Fatalf("settings file changed to %q, read error %v", data, err)
			}
		})
	}
}

func TestSetDisabledRejectsRewriteOverReadCap(t *testing.T) {
	dir := t.TempDir()
	writeWidget(t, dir, "alpha", "title: Alpha\ncommand: [echo, $PORT]\n")
	const prefix = "disabled: ["
	const suffix = "]\n"
	// This hand-edited file is just within the cap. The header and list
	// formatting would push the rewritten file over it.
	original := prefix + strings.Repeat("x", maxSettingsBytes-len(prefix)-len(suffix)) + suffix
	writeSettingsFile(t, dir, original)
	m := NewManager(dir, "/ui")
	defer m.Close()
	if errs := m.LoadErrors(); len(errs) != 0 {
		t.Fatalf("load at exact cap: %v", errs)
	}
	err := m.SetDisabled("alpha", true)
	var partial *DisableStopError
	if err == nil || errors.As(err, &partial) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("SetDisabled error = %v, want uncommitted size error", err)
	}
	if state := widgetState(t, m, "alpha"); state != "stopped" {
		t.Errorf("state after failed write = %q, want stopped", state)
	}
	if data, err := os.ReadFile(filepath.Join(dir, SettingsFile)); err != nil || !bytes.Equal(data, []byte(original)) {
		t.Fatalf("settings file changed (length %d, read error %v)", len(data), err)
	}
}
