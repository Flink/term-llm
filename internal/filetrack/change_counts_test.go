package filetrack

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestChangeCountCacheBoundedLRU(t *testing.T) {
	var cache changeCountCache
	key := func(i int) changeCountKey { return changeCountKey{beforeHash: fmt.Sprint(i), afterHash: "after"} }
	for i := 0; i < maxChangeCountEntries; i++ {
		cache.put(key(i), i, i+1)
	}
	if adds, dels, ok := cache.get(key(0)); !ok || adds != 0 || dels != 1 {
		t.Fatalf("cache hit = %d, %d, %v", adds, dels, ok)
	}
	cache.put(key(maxChangeCountEntries), 2, 3)
	if _, _, ok := cache.get(key(1)); ok {
		t.Fatal("least recently used counts were not evicted")
	}
	if _, _, ok := cache.get(key(0)); !ok {
		t.Fatal("recently accessed counts were evicted")
	}
	cache.put(key(0), 9, 10)
	if adds, dels, ok := cache.get(key(0)); !ok || adds != 9 || dels != 10 {
		t.Fatalf("updated cache hit = %d, %d, %v", adds, dels, ok)
	}
	if len(cache.entries) != maxChangeCountEntries || cache.lru.Len() != maxChangeCountEntries {
		t.Fatalf("cache grew beyond cap: %d entries, %d LRU nodes", len(cache.entries), cache.lru.Len())
	}
}

func TestChangeCountCacheConcurrentAccess(t *testing.T) {
	var cache changeCountCache
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < maxChangeCountEntries*2; i++ {
				key := changeCountKey{beforeHash: fmt.Sprint(i), afterHash: "after"}
				cache.put(key, i, i+1)
				if adds, dels, ok := cache.get(key); ok && (adds != i || dels != i+1) {
					t.Errorf("concurrent cache hit = %d, %d, want %d, %d", adds, dels, i, i+1)
					return
				}
			}
		}()
	}
	workers.Wait()
	if len(cache.entries) > maxChangeCountEntries || cache.lru.Len() != len(cache.entries) {
		t.Fatalf("cache size = %d entries, %d LRU nodes", len(cache.entries), cache.lru.Len())
	}
}

func TestRecentRunCountsReuseHashesAndCheckRetention(t *testing.T) {
	store := openTestStore(t, Options{})
	ctx := context.Background()
	before := []byte(strings.Repeat("before\n", 100))
	after := []byte(strings.Repeat("after\n", 80))
	rec := ChangeRecord{SessionID: "session", RunID: "run-1", Path: "/work/file.txt", Before: before, After: after}
	if _, err := recordTestChange(ctx, store, rec); err != nil {
		t.Fatal(err)
	}
	check := func(sessionID string, runs int, available bool, adds, dels int) CumulativeChange {
		t.Helper()
		changes, err := store.ListRecentRunChanges(ctx, sessionID, runs)
		if err != nil || len(changes) != 1 {
			t.Fatalf("changes = %#v, %v", changes, err)
		}
		got := changes[0]
		if got.ContentAvailable != available || got.Truncated == available || got.Adds != adds || got.Dels != dels {
			t.Fatalf("counts/availability = %#v, want available=%v +%d -%d", got, available, adds, dels)
		}
		return got
	}
	check("session", -1, true, 80, 100)
	if len(store.changeCounts.entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(store.changeCounts.entries))
	}

	// Identical content in another file/session uses the same scalar entry.
	rec.SessionID, rec.Path = "other", "/work/other.txt"
	if _, err := recordTestChange(ctx, store, rec); err != nil {
		t.Fatal(err)
	}
	// Corrupt compressed bodies without removing the hashes. Warm lists must
	// only check existence; attempting to load/decompress would be truncated.
	if _, err := store.db.Exec(`UPDATE blobs SET data=X'00', compression='gzip'`); err != nil {
		t.Fatal(err)
	}
	check("session", -1, true, 80, 100)
	check("other", 1, true, 80, 100)
	if len(store.changeCounts.entries) != 1 {
		t.Fatalf("identical hashes created %d cache entries", len(store.changeCounts.entries))
	}

	// Simulate retention GC of either side while file_changes retains hashes.
	for _, side := range []string{"before_hash", "after_hash"} {
		if _, err := store.db.Exec(`DELETE FROM blobs WHERE hash=(SELECT ` + side + ` FROM file_changes WHERE session_id='session' LIMIT 1)`); err != nil {
			t.Fatal(err)
		}
		check("session", -1, false, 0, 0)
		// Restore the side for the next check. Failed availability must not
		// replace successful counts with cached truncated/zero results.
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range [][]byte{before, after} {
			if _, err := insertBlob(ctx, tx, content); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		check("session", -1, true, 80, 100)
	}

	// A different latest hash and a different baseline must not reuse counts.
	if _, err := recordTestChange(ctx, store, ChangeRecord{
		SessionID: "session", RunID: "run-2", Path: "/work/file.txt", Before: after, After: []byte("latest\n"),
	}); err != nil {
		t.Fatal(err)
	}
	got := check("session", -1, true, 1, 100)
	if got.SnapshotSeq != 2 || got.Seq != 2 {
		t.Fatalf("warm refresh lost sequence identity: %#v", got)
	}
	check("session", 1, true, 1, 80)
	if len(store.changeCounts.entries) != 3 {
		t.Fatalf("different endpoints created %d cache entries, want 3", len(store.changeCounts.entries))
	}
}

func TestRecentRunCountsDoNotCacheUnavailableContent(t *testing.T) {
	for _, failure := range []string{"missing", "corrupt", "unknown"} {
		t.Run(failure, func(t *testing.T) {
			store := openTestStore(t, Options{})
			ctx := context.Background()
			content := []byte("content\n")
			rec := ChangeRecord{SessionID: "session", RunID: "run", Path: "/work/file.txt", BeforeMissing: true, After: content}
			if failure == "unknown" {
				rec.After, rec.AfterUnknown = nil, true
			}
			if _, err := recordTestChange(ctx, store, rec); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing":
				if _, err := store.db.Exec(`DELETE FROM blobs`); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if _, err := store.db.Exec(`UPDATE blobs SET data=X'00', compression='gzip'`); err != nil {
					t.Fatal(err)
				}
			}
			changes, err := store.ListRecentRunChanges(ctx, "session", -1)
			if err != nil || len(changes) != 1 || changes[0].ContentAvailable || !changes[0].Truncated || changes[0].Adds != 0 || changes[0].Dels != 0 {
				t.Fatalf("unavailable content = %#v, %v", changes, err)
			}
			if len(store.changeCounts.entries) != 0 {
				t.Fatal("unavailable result entered the count cache")
			}
			if failure == "unknown" {
				if _, err := recordTestChange(ctx, store, ChangeRecord{
					SessionID: "session", RunID: "run-2", Path: rec.Path, Before: []byte("observed before\n"), After: content,
				}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.db.Exec(`INSERT OR REPLACE INTO blobs(hash,size,compression,data)
				SELECT after_hash,?,'none',? FROM file_changes WHERE session_id='session'`, len(content), content); err != nil {
				t.Fatal(err)
			}
			changes, err = store.ListRecentRunChanges(ctx, "session", -1)
			if err != nil || len(changes) != 1 || !changes[0].ContentAvailable || changes[0].Truncated || changes[0].Adds != 1 || changes[0].Dels != 0 {
				t.Fatalf("recovered content = %#v, %v", changes, err)
			}
		})
	}
}

func TestRecentRunCountsWarmRefreshBeyond512Files(t *testing.T) {
	store := openTestStore(t, Options{})
	ctx := context.Background()
	const files = 600
	for file := 0; file < files; file++ {
		if _, err := recordTestChange(ctx, store, ChangeRecord{
			SessionID: "session", RunID: "run", Path: fmt.Sprintf("/work/file-%d.txt", file),
			Before: []byte(fmt.Sprintf("before %d\n", file)), After: []byte(fmt.Sprintf("after %d\n", file)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		changes, err := store.ListRecentRunChanges(ctx, "session", -1)
		if err != nil || len(changes) != files {
			t.Fatalf("all-turn refresh = %d files, %v", len(changes), err)
		}
		for _, change := range changes {
			if !change.ContentAvailable || change.Truncated || change.Adds != 1 || change.Dels != 1 {
				t.Fatalf("warm count not retained: %#v", change)
			}
		}
	}
	check()
	// Existing keys must serve every file without decompression after warming
	// more than 512 unique pairs, rather than cyclically evicting each other.
	if _, err := store.db.Exec(`UPDATE blobs SET data=X'00', compression='gzip'`); err != nil {
		t.Fatal(err)
	}
	check()
	check()
}
