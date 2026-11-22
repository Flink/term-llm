package poll

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage"
)

func openTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSettingsPersistence(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil || c.Snapshot().IntervalMinutes != 15 {
		t.Fatalf("default settings: %+v, %v", c.Snapshot(), err)
	}
	for _, minutes := range []int{10, 1440} {
		if err := c.SetInterval(minutes); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dir)
		if err != nil || reopened.Snapshot().IntervalMinutes != minutes {
			t.Fatalf("settings after restart: %+v, %v", reopened.Snapshot(), err)
		}
	}
	for _, minutes := range []int{-1, 9, 1441} {
		if err := c.SetInterval(minutes); !errors.Is(err, ErrInterval) {
			t.Fatalf("SetInterval(%d): %v", minutes, err)
		}
	}
	if err := os.Remove(c.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.SetInterval(15); err == nil || c.Snapshot().IntervalMinutes != 1440 {
		t.Fatalf("failed save changed settings: %+v, %v", c.Snapshot(), err)
	}
	if reopened, err := Open(dir); err == nil || reopened.Snapshot().IntervalMinutes != 15 {
		t.Fatalf("invalid preferences did not fall back: %+v, %v", reopened.Snapshot(), err)
	}
}

func TestScheduledPollingReschedulesOnIntervalChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := openTestCache(t)
		var calls atomic.Int32
		c.Reconcile([]Source{{ID: "one", Key: "private", Fetch: func(context.Context) (nativeusage.Report, error) {
			calls.Add(1)
			return nativeusage.Report{Plan: "fixture"}, nil
		}}})
		if c.Snapshot().Refreshing {
			t.Fatal("unstarted service is not refreshing")
		}
		c.Start(t.Context())
		defer c.Close()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("startup calls = %d", calls.Load())
		}
		time.Sleep(14 * time.Minute)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("polled before interval: %d", calls.Load())
		}
		if err := c.SetInterval(10); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("overdue poll not run immediately: %d", calls.Load())
		}
		time.Sleep(3 * time.Minute)
		if err := c.SetInterval(12); err != nil {
			t.Fatal(err)
		}
		time.Sleep(9*time.Minute - time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("did not reschedule old deadline: %d", calls.Load())
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatalf("missed new deadline: %d", calls.Load())
		}
	})
}

func TestManualRefreshCoalescesAndPreservesLastSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := openTestCache(t)
		var calls atomic.Int32
		c.Reconcile([]Source{{ID: "one", Key: "private", Fetch: func(ctx context.Context) (nativeusage.Report, error) {
			switch calls.Add(1) {
			case 2:
				<-ctx.Done()
				return nativeusage.Report{}, errors.New("raw credentials must not be exposed")
			case 3:
				return nativeusage.Report{}, errors.New("raw response must not be exposed")
			default:
				return nativeusage.Report{Plan: "sample"}, nil
			}
		}}})
		c.Start(t.Context())
		defer c.Close()
		synctest.Wait()
		first := c.Snapshot().Providers[0]
		requestRefresh(t, c)
		synctest.Wait()
		if !c.Snapshot().Refreshing {
			t.Fatal("expected in-flight manual refresh")
		}
		for range 5 {
			requestRefresh(t, c)
		}
		time.Sleep(fetchTimeout)
		synctest.Wait()
		assertStale(t, c, first, "Usage refresh timed out")
		var cooldown *CooldownError
		if err := c.Refresh(); !errors.As(err, &cooldown) || cooldown.RetryAfter != manualCooldown-fetchTimeout {
			t.Fatalf("cooldown: %v", err)
		}
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("overlapping requests or cooldown queued extra calls: %d", calls.Load())
		}
		time.Sleep(manualCooldown - fetchTimeout)
		requestRefresh(t, c)
		synctest.Wait()
		assertStale(t, c, first, "Provider usage is temporarily unavailable")
		time.Sleep(manualCooldown)
		requestRefresh(t, c)
		synctest.Wait()
		got := c.Snapshot().Providers[0]
		if got.Error != "" || !got.UpdatedAt.After(*first.UpdatedAt) {
			t.Fatalf("success did not clear stale state: %+v", got)
		}
	})
}

func assertStale(t *testing.T, c *Cache, first Provider, message string) {
	t.Helper()
	current := c.Snapshot()
	got := current.Providers[0]
	if current.Refreshing || got.Report != first.Report || got.UpdatedAt != first.UpdatedAt || got.Error != message {
		t.Fatalf("failure did not preserve the last success: %+v", current)
	}
}

func requestRefresh(t *testing.T, c *Cache) {
	t.Helper()
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileRejectsSupersededResultsAndCloseJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := openTestCache(t)
		oldCanceled, newCanceled := make(chan struct{}), make(chan struct{})
		c.Reconcile([]Source{{ID: "one", Key: "old", Fetch: func(ctx context.Context) (nativeusage.Report, error) {
			<-ctx.Done()
			close(oldCanceled)
			return nativeusage.Report{Plan: "old"}, nil
		}}})
		c.Start(t.Context())
		defer c.Close()
		synctest.Wait()
		c.Reconcile([]Source{{ID: "one", Key: "new", Fetch: func(ctx context.Context) (nativeusage.Report, error) {
			<-ctx.Done()
			close(newCanceled)
			return nativeusage.Report{Plan: "new"}, nil
		}}})
		synctest.Wait()
		select {
		case <-oldCanceled:
		default:
			t.Fatal("old identity not canceled")
		}
		got := c.Snapshot()
		if !got.Refreshing || got.Providers[0].Report != nil || got.Providers[0].Error != "" {
			t.Fatalf("new identity inherited the old result: %+v", got)
		}
		c.Close()
		select {
		case <-newCanceled:
		default:
			t.Fatal("Close returned before fetch exited")
		}
		if c.Snapshot().Refreshing || !errors.Is(c.Refresh(), ErrClosed) {
			t.Fatal("closed cache still accepts refreshes")
		}
	})
}
