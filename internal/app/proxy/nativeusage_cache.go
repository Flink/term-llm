package proxy

import (
	"log"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage/poll"
)

func (s *Service) openNativeUsage() {
	cache, err := poll.Open(s.opts.StateDir)
	if err != nil {
		log.Print("proxy: native usage interval unavailable; using the default")
	}
	s.nativeUsage = cache
	s.reconcileNativeUsageLocked()
}

// Every committed snapshot edit publishes its source changes together. Startup
// calls this before sharing the service; otherwise the caller holds s.mu.
func (s *Service) publishSnapshotLocked(next *snapshot) {
	s.snap = next
	s.reconcileNativeUsageLocked()
}

func (s *Service) reconcileNativeUsageLocked() {
	if s.nativeUsage == nil || s.closing {
		return
	}
	s.nativeUsage.Reconcile(s.nativeUsageSourcesLocked())
}
