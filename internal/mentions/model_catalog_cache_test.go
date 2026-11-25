package mentions

import (
	"fmt"
	"testing"
	"time"
)

func TestMentionModelCacheBoundsAndFreshness(t *testing.T) {
	var cache ModelCatalogCache
	now := time.Now()
	started := map[string]*modelCatalogEntry{}
	snapshot := func(provider string, now time.Time) ([]ModelOption, bool) {
		return cache.snapshot(provider, now, func(entry *modelCatalogEntry) { started[provider] = entry })
	}
	for i := range modelCatalogMaxFlights + 1 {
		if _, pending := snapshot(fmt.Sprint(i), now); !pending {
			t.Fatal("saturated lookup should remain pending")
		}
	}
	if len(started) != modelCatalogMaxFlights {
		t.Fatalf("started %d refreshes, want %d", len(started), modelCatalogMaxFlights)
	}
	first := started["0"]
	options := []ModelOption{{ID: "old"}}
	cache.finish(first, options, now.Add(modelCatalogFreshness))
	if got, pending := snapshot("0", now); pending || len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("fresh snapshot = %v, pending = %v", got, pending)
	}
	expired := now.Add(modelCatalogFreshness)
	if got, pending := snapshot("0", expired); !pending || len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("stale snapshot = %v, pending = %v", got, pending)
	}
	cache.finish(first, nil, expired.Add(modelCatalogRetryDelay))
	if got, pending := snapshot("0", expired); pending || len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("failed refresh snapshot = %v, pending = %v", got, pending)
	}
	for i := modelCatalogMaxFlights; i < modelCatalogMaxEntries; i++ {
		id := fmt.Sprint(i)
		snapshot(id, expired)
		cache.finish(started[id], options, expired.Add(modelCatalogFreshness))
	}
	if _, pending := snapshot("overflow", expired); pending || len(cache.entries) != modelCatalogMaxEntries {
		t.Fatalf("all-fresh cache = %d entries, pending = %v", len(cache.entries), pending)
	}
	if _, pending := snapshot("overflow", expired.Add(modelCatalogFreshness)); !pending || len(cache.entries) != modelCatalogMaxEntries || cache.entries["overflow"] == nil {
		t.Fatalf("expired cache did not make room: %d entries, pending = %v", len(cache.entries), pending)
	}
}
