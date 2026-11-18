package proxy

import (
	"context"
	"log"
	"os"
	"strings"
	"time"
)

// Run owns this worker; Open and request finalization only load/read snapshots.
// Failed refreshes leave cached rates available and retry without delaying traffic.
func (s *Service) startPricingRefresh(ctx context.Context) func() {
	if s.pricing == nil || strings.TrimSpace(os.Getenv("TERM_LLM_OFFLINE")) != "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		warned := false
		for {
			refresh, c := context.WithTimeout(ctx, 30*time.Second)
			err := s.pricing.Refresh(refresh)
			c()
			if err != nil && ctx.Err() == nil {
				if !warned {
					log.Print("proxy: models.dev refresh unavailable; keeping cached or bundled rates")
				}
				warned = true
			} else if err == nil {
				warned = false
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
