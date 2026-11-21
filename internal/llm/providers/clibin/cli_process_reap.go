package clibin

import (
	"syscall"
	"time"

	"github.com/term-llm/term-llm/internal/procutil"
)

// cliGroupKillRetry is how often a stopping CLI's process group is killed
// again while its stdout stays open.
const cliGroupKillRetry = 100 * time.Millisecond

// awaitCLIScanner waits for the stdout scanner goroutine to finish. Once
// stopping is closed the owned process group has already been killed, but a
// descendant the CLI was forking at that moment can escape that kill: it
// lands in the group after the kernel enumerated it, is reparented to init and
// keeps stdout open (observed on macOS). Keep killing the group until stdout
// reaches EOF so the turn ends promptly.
func awaitCLIScanner(scanErrCh <-chan error, stopping <-chan struct{}, owned *procutil.Command) error {
	select {
	case err := <-scanErrCh:
		return err
	case <-stopping:
	}
	ticker := time.NewTicker(cliGroupKillRetry)
	defer ticker.Stop()
	for {
		select {
		case err := <-scanErrCh:
			return err
		case <-ticker.C:
			_ = owned.Signal(syscall.SIGKILL)
		}
	}
}

// closedCLIStop is a stopping channel for callers that have already killed the
// process tree.
var closedCLIStop = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()
