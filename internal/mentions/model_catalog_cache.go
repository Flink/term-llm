package mentions

import (
	"context"
	"sync"
	"time"
)

const (
	modelCatalogMaxEntries = 64
	modelCatalogMaxFlights = 8
	modelCatalogTimeout    = 15 * time.Second
	modelCatalogFreshness  = 2 * time.Minute
	modelCatalogRetryDelay = 30 * time.Second
)

// ModelCatalogCache returns snapshots without waiting for live discovery. It
// coalesces refreshes by provider and limits the number of concurrent refreshes.
// The zero value is ready to use.
type ModelCatalogCache struct {
	mu      sync.Mutex
	entries map[string]*modelCatalogEntry
	flights int
	closed  bool
	idle    chan struct{}
}

type modelCatalogEntry struct {
	options    []ModelOption
	refreshAt  time.Time
	refreshing bool
}

// Snapshot returns immutable options (nil if not loaded) and whether another
// lookup may see additional results. load runs outside the request lifetime,
// with a timeout. A nil result retains the previous options and backs off.
func (c *ModelCatalogCache) Snapshot(provider string, now time.Time, load func(context.Context, string) []ModelOption) ([]ModelOption, bool) {
	return c.snapshot(provider, now, func(entry *modelCatalogEntry) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), modelCatalogTimeout)
			defer cancel()
			options := load(ctx, provider)
			delay := modelCatalogFreshness
			if options == nil {
				delay = modelCatalogRetryDelay
			}
			c.finish(entry, options, time.Now().Add(delay))
		}()
	})
}

func (c *ModelCatalogCache) snapshot(provider string, now time.Time, start func(*modelCatalogEntry)) ([]ModelOption, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[provider]
	if c.closed {
		return nil, false
	}
	if entry != nil && (entry.refreshing || now.Before(entry.refreshAt)) {
		return entry.options, entry.refreshing
	}
	if c.flights >= modelCatalogMaxFlights {
		// Do not enqueue keystrokes. A client revalidation can use a slot
		// freed by earlier requests, or read the new snapshot immediately.
		if entry != nil {
			return entry.options, true
		}
		return nil, true
	}
	if entry == nil {
		if !c.makeRoom(now) {
			return nil, false
		}
		entry = &modelCatalogEntry{}
		if c.entries == nil {
			c.entries = make(map[string]*modelCatalogEntry)
		}
		c.entries[provider] = entry
	}
	entry.refreshing = true
	if c.flights == 0 {
		c.idle = make(chan struct{})
	}
	c.flights++
	start(entry)
	return entry.options, true
}

// makeRoom never evicts an in-flight or fresh entry: repeated broad searches
// above the cache limit should use local fallbacks rather than churn upstream.
func (c *ModelCatalogCache) makeRoom(now time.Time) bool {
	if len(c.entries) < modelCatalogMaxEntries {
		return true
	}
	var oldest string
	for provider, entry := range c.entries {
		if !entry.refreshing && !now.Before(entry.refreshAt) && (oldest == "" || entry.refreshAt.Before(c.entries[oldest].refreshAt)) {
			oldest = provider
		}
	}
	if oldest == "" {
		return false
	}
	delete(c.entries, oldest)
	return true
}

func (c *ModelCatalogCache) finish(entry *modelCatalogEntry, options []ModelOption, refreshAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if options != nil {
		entry.options = options
	}
	entry.refreshAt = refreshAt
	entry.refreshing = false
	c.flights--
	if c.flights == 0 {
		close(c.idle)
		c.idle = nil
	}
}

// Close prevents new loads and returns a channel closed when all loads finish.
func (c *ModelCatalogCache) Close() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.idle != nil {
		return c.idle
	}
	done := make(chan struct{})
	close(done)
	return done
}

// CloseAndDrain runs cleanup once this cache has no active loads. A shutdown
// deadline stops waiting but leaves cleanup scheduled after the last load.
func (c *ModelCatalogCache) CloseAndDrain(ctx context.Context, cleanup func()) error {
	drained := c.Close()
	done := make(chan struct{})
	go func() {
		<-drained
		cleanup()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
