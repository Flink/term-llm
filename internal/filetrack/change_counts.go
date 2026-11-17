package filetrack

import (
	"container/list"
	"context"
	"sync"
)

// Bound memory independently of the number of sessions, files, and revisions.
// Only hashes and scalar counts are retained, never file contents or errors.
const maxChangeCountEntries = 4096

type changeCountKey struct {
	beforeHash string
	afterHash  string
}

type changeCountEntry struct {
	key        changeCountKey
	adds, dels int
}

type changeCountCache struct {
	mu      sync.Mutex
	entries map[changeCountKey]*list.Element
	lru     list.List
}

func (c *changeCountCache) get(key changeCountKey) (adds, dels int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element := c.entries[key]; element != nil {
		c.lru.MoveToFront(element)
		entry := element.Value.(changeCountEntry)
		return entry.adds, entry.dels, true
	}
	return 0, 0, false
}

func (c *changeCountCache) put(key changeCountKey, adds, dels int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := changeCountEntry{key: key, adds: adds, dels: dels}
	if element := c.entries[key]; element != nil {
		element.Value = entry
		c.lru.MoveToFront(element)
		return
	}
	if c.entries == nil {
		c.entries = make(map[changeCountKey]*list.Element)
	}
	c.entries[key] = c.lru.PushFront(entry)
	if c.lru.Len() > maxChangeCountEntries {
		oldest := c.lru.Back()
		delete(c.entries, oldest.Value.(changeCountEntry).key)
		c.lru.Remove(oldest)
	}
}

// cachedSpanCounts rechecks retention even on cache hits: GC can remove blobs
// without changing the hashes in file_changes. Failed or incomplete reads never
// populate the cache. Concurrent misses may compute twice, but neither SQLite
// reads nor line diffing hold the cache lock or serialize unrelated files.
func (s *Store) cachedSpanCounts(ctx context.Context, sp *pathSpan, kind string) (adds, dels int, available bool) {
	needBefore, needAfter := blobsNeeded(kind)
	var key changeCountKey
	if needBefore {
		key.beforeHash = sp.firstBeforeHash
		if key.beforeHash == "" {
			return 0, 0, false
		}
	}
	if needAfter {
		key.afterHash = sp.lastAfterHash
		if key.afterHash == "" {
			return 0, 0, false
		}
	}
	if adds, dels, ok := s.changeCounts.get(key); ok {
		if (needBefore && !s.blobExists(ctx, key.beforeHash)) || (needAfter && !s.blobExists(ctx, key.afterHash)) {
			return 0, 0, false
		}
		return adds, dels, true
	}

	// Cold getBlob reads already verify existence; avoid redundant probes.
	var before, after []byte
	var err error
	if needBefore {
		if before, err = s.getBlob(ctx, key.beforeHash); err != nil {
			return 0, 0, false
		}
	}
	if needAfter {
		if after, err = s.getBlob(ctx, key.afterHash); err != nil {
			return 0, 0, false
		}
	}
	if _, isImage := imageChangeMediaType(kind, before, after); !isImage {
		adds, dels = CountAddsDels(before, after)
	}
	s.changeCounts.put(key, adds, dels)
	return adds, dels, true
}
