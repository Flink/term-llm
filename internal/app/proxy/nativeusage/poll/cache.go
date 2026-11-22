// Package poll maintains a bounded, in-memory cache of native subscription usage.
package poll

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage"
)

const (
	fetchTimeout   = 20 * time.Second
	maxConcurrent  = 8
	manualCooldown = time.Minute
)

var ErrClosed = errors.New("usage cache is closed")

// Source.Key is a private identity: results survive edits that leave the ID
// and key unchanged, but are discarded when either changes.
// Fetch must honor context cancellation and transfer ownership of its report.
type Source struct {
	ID, Key string
	Route   string // Public route label, set only when this provider has multiple bindings.
	Fetch   func(context.Context) (nativeusage.Report, error)
}

type bindingID struct{ provider, key string }

func (s Source) binding() bindingID { return bindingID{s.ID, s.Key} }

type Provider struct {
	Route     string              `json:"route,omitempty"`
	ID        string              `json:"id"`
	Report    *nativeusage.Report `json:"report,omitempty"`
	UpdatedAt *time.Time          `json:"updated_at,omitempty"`
	Error     string              `json:"error,omitempty"`
}

type Snapshot struct {
	IntervalMinutes int        `json:"interval_minutes"`
	Providers       []Provider `json:"providers"`
	Refreshing      bool       `json:"refreshing"`
}

type entry struct {
	source  Source
	result  Provider
	pending bool
	active  bool
	cancel  context.CancelFunc
}

// Cache has one scheduler and no more than maxConcurrent active Fetch calls.
// Reads never discover sources or contact upstream providers.
type Cache struct {
	mu          sync.Mutex
	entries     map[bindingID]*entry
	interval    int
	nextPoll    time.Time
	manualAfter time.Time
	wake        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	closed      bool

	settingsMu sync.Mutex
	path       string
}

func (c *Cache) Start(parent context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.ctx, c.cancel, c.done = ctx, cancel, make(chan struct{})
	c.nextPoll = time.Now().Add(c.period())
	go c.run(ctx, c.done)
}

func (c *Cache) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	done := c.done
	if done != nil {
		c.cancel()
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	// Allow an already-started durable settings update to finish.
	c.settingsMu.Lock()
	c.settingsMu.Unlock()
}

func (c *Cache) Reconcile(sources []Source) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	next := make(map[bindingID]*entry, len(sources))
	for _, source := range sources {
		id := source.binding()
		if e := c.entries[id]; e != nil {
			e.source = source
			e.result.Route = source.Route
			next[id] = e
		} else {
			next[id] = &entry{source: source, result: Provider{ID: source.ID, Route: source.Route}, pending: true}
		}
	}
	for id, old := range c.entries {
		if old != next[id] && old.cancel != nil {
			old.cancel()
		}
	}
	c.entries = next
	c.signal()
}

func (c *Cache) HasSources() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries) != 0
}

func (c *Cache) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Snapshot{IntervalMinutes: c.interval, Providers: make([]Provider, 0, len(c.entries))}
	for _, e := range c.entries {
		out.Providers = append(out.Providers, e.result)
		out.Refreshing = out.Refreshing || e.pending || e.active
	}
	slices.SortFunc(out.Providers, func(a, b Provider) int { return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Route, b.Route)) })
	out.Refreshing = out.Refreshing && !c.closed && c.ctx != nil && c.ctx.Err() == nil
	return out
}

type CooldownError struct {
	RetryAfter time.Duration
}

func (*CooldownError) Error() string { return "Usage refresh is on cooldown" }

// Refresh accepts or joins a batch without waiting for upstream. A completed
// manual refresh still suppresses repeated requests until the cooldown ends.
func (c *Cache) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping() {
		return ErrClosed
	}
	now := time.Now()
	if len(c.entries) == 0 {
		return nil
	}
	for _, e := range c.entries {
		if e.pending || e.active {
			if !now.Before(c.manualAfter) {
				c.manualAfter = now.Add(manualCooldown)
			}
			return nil
		}
	}
	if now.Before(c.manualAfter) {
		return &CooldownError{RetryAfter: c.manualAfter.Sub(now)}
	}
	c.manualAfter = now.Add(manualCooldown)
	c.queueAll()
	c.nextPoll = now.Add(c.period())
	c.signal()
	return nil
}

// stopping is called while holding c.mu.
func (c *Cache) stopping() bool { return c.closed || c.ctx != nil && c.ctx.Err() != nil }

func (c *Cache) queueAll() {
	for _, e := range c.entries {
		e.pending = true
	}
}

func (c *Cache) setInterval(minutes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.interval != minutes {
		// Keep the previous poll as the anchor, not the settings edit.
		lastPoll := c.nextPoll.Add(-c.period())
		c.interval = minutes
		c.nextPoll = lastPoll.Add(c.period())
		c.signal()
	}
}

func (c *Cache) period() time.Duration { return time.Duration(c.interval) * time.Minute }

func (c *Cache) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Cache) work() (batch []*entry, delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if !now.Before(c.nextPoll) {
		c.queueAll()
		c.nextPoll = now.Add(c.period())
	}
	for _, e := range c.entries {
		if e.pending {
			e.pending, e.active = false, true
			batch = append(batch, e)
		}
	}
	return batch, c.nextPoll.Sub(now)
}

func (c *Cache) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for ctx.Err() == nil {
		batch, delay := c.work()
		if len(batch) != 0 {
			c.fetchBatch(ctx, batch)
			continue
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
		case <-c.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (c *Cache) fetchBatch(ctx context.Context, batch []*entry) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrent)
	for _, e := range batch {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Go(func() {
			defer func() { <-sem }()
			c.fetch(ctx, e)
		})
	}
	wg.Wait()
}

func (c *Cache) fetch(parent context.Context, e *entry) {
	c.mu.Lock()
	source := e.source
	if parent.Err() != nil || c.entries[source.binding()] != e {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, fetchTimeout)
	e.cancel = cancel
	c.mu.Unlock()

	report, err := source.Fetch(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	e.cancel, e.active = nil, false
	if parent.Err() != nil || c.entries[source.binding()] != e {
		return
	}
	// Never expose raw upstream errors (which may contain authentication or
	// endpoint details). A failed attempt does not overwrite the last success.
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		e.result.Error = "Usage refresh timed out"
	case err != nil:
		e.result.Error = "Provider usage is temporarily unavailable"
	default:
		now := time.Now().UTC()
		e.result = Provider{ID: source.ID, Route: e.source.Route, Report: &report, UpdatedAt: &now}
	}
}
