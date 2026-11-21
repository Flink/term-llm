package widgets

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/term-llm/term-llm/internal/procutil"
)

func TestQueuedDisableStopRespectsEnable(t *testing.T) {
	for _, source := range []string{"reload", "direct"} {
		t.Run(source, func(t *testing.T) {
			// An external timeout bounds the readiness loop even while the
			// queued stop is blocked on a mutex, freezing the bubble clock.
			timeout := time.NewTimer(5 * time.Second)
			defer timeout.Stop()
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				writeWidget(t, dir, "alpha", "title: Alpha\ncommand: [echo, $PORT]\n")
				m := NewManager(dir, "/ui")
				defer m.Close()
				e := m.entries["alpha"]
				owned := &procutil.Command{}
				done := make(chan struct{})
				close(done)
				e.state, e.owned, e.procDone = stateRunning, owned, done

				// Hold the lifecycle lock until both preferences have committed.
				err := func() error {
					e.startMu.Lock()
					defer e.startMu.Unlock()
					if source == "reload" {
						writeSettingsFile(t, dir, "disabled: [alpha]\n")
						if errs := m.Reload(); len(errs) != 0 {
							return fmt.Errorf("reload: %v", errs)
						}
					} else {
						go func() {
							if err := m.SetDisabled("alpha", true); err != nil {
								t.Errorf("disable: %v", err)
							}
						}()
						// The committed disabled state signals that SetDisabled has
						// done its settings work and cannot stop past startMu yet.
						for widgetState(t, m, "alpha") != "disabled" {
							select {
							case <-timeout.C:
								return errors.New("disable did not commit while the lifecycle lock was held")
							default:
								runtime.Gosched()
							}
						}
					}
					return m.SetDisabled("alpha", false)
				}()
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if state := widgetState(t, m, "alpha"); state != "running" || e.owned != owned {
					t.Fatalf("stale %s disable stopped the enabled widget: %s", source, state)
				}
			})
		})
	}
}

func TestDisableStopErrorPreservesPreferenceAndDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeWidget(t, dir, "alpha", "title: Alpha\ncommand: [echo, $PORT]\n")
		m := NewManager(dir, "/ui")
		defer m.Close()
		e := m.entries["alpha"]
		owned := &procutil.Command{}
		e.state, e.owned = stateRunning, owned // deliberately no exit acknowledgement

		err := m.SetDisabled("alpha", true)
		var partial *DisableStopError
		if !errors.As(err, &partial) || errors.Unwrap(partial) == nil {
			t.Fatalf("SetDisabled error = %v, want wrapped *DisableStopError", err)
		}
		settings, err := readSettings(dir)
		if err != nil || !slices.Equal(settings.Disabled, []string{"alpha"}) {
			t.Fatalf("committed settings = %+v, %v", settings, err)
		}
		if s := m.Status()[0]; s.State != "disabled" || s.Error != partial.Err.Error() {
			t.Fatalf("status lost stop diagnostic: %+v", s)
		}
		if rec := proxyGet(m, "alpha"); rec.Code != http.StatusNotFound {
			t.Fatalf("disabled proxy request = %d %q, want 404", rec.Code, rec.Body.String())
		}

		// A reload must not clear an error while it still owns the process,
		// and should retry the stop for a disabled entry.
		done := make(chan struct{})
		close(done)
		e.startMu.Lock()
		e.procDone = done
		errs := m.Reload()
		status := m.Status()[0]
		e.startMu.Unlock()
		if len(errs) != 0 || status.State != "disabled" || status.Error != partial.Err.Error() {
			t.Fatalf("status while reload stop is queued: %+v, reload errors %v", status, errs)
		}
		synctest.Wait()
		if s := m.Status()[0]; s.State != "disabled" || s.Error != "" || e.owned != nil {
			t.Fatalf("reload did not finish stopping owned process: %+v", s)
		}
	})
}

func TestReloadRecoversEnabledWidgetAfterFailedStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeWidget(t, dir, "alpha", "title: Alpha\ncommand: [echo, $PORT]\n")
		m := NewManager(dir, "/ui")
		defer m.Close()
		e := m.entries["alpha"]
		e.state, e.owned = stateRunning, &procutil.Command{}
		var partial *DisableStopError
		if err := m.SetDisabled("alpha", true); !errors.As(err, &partial) {
			t.Fatalf("disable = %v, want a partial stop error", err)
		}
		if err := m.SetDisabled("alpha", false); err != nil {
			t.Fatal(err)
		}
		e.procDone = make(chan struct{})
		close(e.procDone)
		if errs := m.Reload(); len(errs) != 0 {
			t.Fatal(errs)
		}
		synctest.Wait()
		if got := widgetState(t, m, "alpha"); got != "stopped" || e.owned != nil {
			t.Fatalf("reload did not recover enabled widget: state=%s, owned=%v", got, e.owned)
		}
	})
}

func TestReloadDisablesLiveWidget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper widget process is exercised on Unix")
	}
	dir := t.TempDir()
	writeWidget(t, dir, "live", fmt.Sprintf("title: Live\ncommand: [%q, '-test.run=^TestWidgetChildHTTPServer$', '--', '$PORT']\n", os.Args[0]))
	m := NewManager(dir, "/ui")
	defer m.Close()
	if rec := proxyGet(m, "live"); rec.Code != http.StatusOK {
		t.Fatalf("initial request = %d %q, want 200", rec.Code, rec.Body.String())
	}
	e := m.entries["live"]
	e.mu.Lock()
	oldProc, done := e.processLocked(), e.procDone
	e.mu.Unlock()
	if oldProc == nil || done == nil {
		t.Fatal("widget process did not start")
	}

	writeSettingsFile(t, dir, "disabled: [live]\n")
	if errs := m.Reload(); len(errs) != 0 {
		t.Fatalf("disable reload: %v", errs)
	}
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("reload did not stop the live widget")
	}
	if rec := proxyGet(m, "live"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "widget disabled") {
		t.Fatalf("request while disabled = %d %q, want widget-disabled 404", rec.Code, rec.Body.String())
	}

	if err := os.Remove(filepath.Join(dir, SettingsFile)); err != nil {
		t.Fatal(err)
	}
	if errs := m.Reload(); len(errs) != 0 {
		t.Fatalf("enable reload: %v", errs)
	}
	if state := widgetState(t, m, "live"); state != "stopped" {
		t.Fatalf("state after enable = %s, want stopped", state)
	}
	if rec := proxyGet(m, "live"); rec.Code != http.StatusOK {
		t.Fatalf("request after enable = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if proc := e.process(); proc == nil || proc.Pid == oldProc.Pid {
		t.Fatalf("reload did not allow a fresh widget process: %v", proc)
	}
}
