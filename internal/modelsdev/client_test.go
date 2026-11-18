package modelsdev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeSnapshot(t *testing.T, dir string, at time.Time, body string) {
	t.Helper()
	catalog, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewClient(dir, nil).save(catalog, at); err != nil {
		t.Fatal(err)
	}
}

func TestClientRefreshPersistsAndLoadsWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	client := NewClient(dir, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return fixtureClient(fixture).Transport.RoundTrip(req)
	})})
	client.Endpoint = "https://catalog.example/api.json"
	before := time.Now()
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	model, fetchedAt, ok := client.Lookup("anthropic", "claude-haiku-5-5")
	if !ok || model.Name != "Claude Haiku 5.5" || fetchedAt.Before(before) || fetchedAt.After(time.Now()) {
		t.Fatalf("lookup = %+v %v %v", model, fetchedAt, ok)
	}
	for _, key := range [][2]string{{"Anthropic", "claude-haiku-5-5"}, {"anthropic", "CLAUDE-HAIKU-5-5"}, {"anthropic", "haiku"}, {"missing", "claude-haiku-5-5"}} {
		if _, _, found := client.Lookup(key[0], key[1]); found {
			t.Fatalf("exact lookup accepted alias %v", key)
		}
	}
	if *model.Cost.CacheWrite != 0.125 {
		t.Fatal("published baseline changed to a TTL-specific cache-write price")
	}
	// Return values cannot mutate shared state, including nested tier pointers.
	*model.Cost.Input = 99
	*model.Cost.Tiers[0].CacheWrite = 99
	catalog, _ := client.Snapshot()
	*catalog["anthropic"].Models["claude-haiku-5-5"].Cost.Output = 99
	delete(catalog, "reseller")
	unmodified, _, _ := client.Lookup("anthropic", "claude-haiku-5-5")
	if *unmodified.Cost.Input != 0.1 || *unmodified.Cost.Output != 0.5 || *unmodified.Cost.Tiers[0].CacheWrite != 0 {
		t.Fatal("caller mutation changed the shared snapshot")
	}
	if _, _, ok := client.Lookup("reseller", "claude-haiku-5-5"); !ok {
		t.Fatal("caller mutation removed provider from shared snapshot")
	}
	// A fresh client loads the source timestamp and every pointer component.
	loaded := NewClient(dir, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("Load/Lookup/fresh Refresh must not fetch")
		return nil, errors.New("network forbidden")
	})})
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, persistedAt, found := loaded.Lookup("anthropic", "claude-haiku-5-5")
	if !found || !persistedAt.Equal(fetchedAt) || *persisted.Cost.Input != 0.1 || *persisted.Cost.Tiers[0].CacheWrite != 0 || persisted.Cost.Tiers[0].CacheRead != nil || persisted.Cost.Tiers[0].Tier.Size != 100000 || persisted.Cost.Tiers[0].Tier.Type != "context" {
		t.Fatalf("disk round trip = %+v at %v", persisted, persistedAt)
	}
	if _, found := loaded.ProviderForAPI("https://API.ANTHROPIC.com/v1/"); !found {
		t.Fatal("provider API metadata did not survive disk round trip")
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch count = %d", calls.Load())
	}
}

func TestClientRetainsLastGoodSnapshotOnFailure(t *testing.T) {
	for _, badBody := range []string{"fetch failure", "invalid json", `{"p":{"models":{}}}`} {
		t.Run(badBody, func(t *testing.T) {
			dir := t.TempDir()
			old := time.Now().UTC().Add(-TTL - time.Minute)
			writeSnapshot(t, dir, old, fixture)
			client := NewClient(dir, fixtureClient(badBody))
			if badBody == "fetch failure" {
				client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("unavailable")
				})}
			}
			// Touching the file must not fabricate a fresh source timestamp.
			now := time.Now()
			if err := os.Chtimes(filepath.Join(dir, CacheFile), now, now); err != nil {
				t.Fatal(err)
			}
			if err := client.Refresh(context.Background()); err == nil {
				t.Fatal("expected refresh failure")
			}
			if _, at, ok := client.Lookup("anthropic", "claude-haiku-5-5"); !ok || !at.Equal(old) {
				t.Fatalf("last good snapshot lost: %v %v", at, ok)
			}
			reloaded := NewClient(dir, nil)
			if err := reloaded.Load(); err != nil {
				t.Fatal(err)
			}
			if _, at, ok := reloaded.Lookup("anthropic", "claude-haiku-5-5"); !ok || !at.Equal(old) {
				t.Fatal("failed fetch overwrote disk snapshot")
			}
		})
	}
}

func TestClientLoadRejectsInvalidCacheWithoutReplacingSnapshot(t *testing.T) {
	dir := t.TempDir()
	client := NewClient(dir, fixtureClient(fixture))
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"version":999,"fetched_at":"2026-01-01T00:00:00Z","catalog":{}}`,
		`{"version":1,"catalog":{"p":{"models":{}}}}`,
		`null`, `broken`,
	} {
		if err := os.WriteFile(filepath.Join(dir, CacheFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := client.Load(); err == nil {
			t.Fatalf("invalid disk snapshot accepted: %s", body)
		}
		if _, _, ok := client.Lookup("anthropic", "claude-haiku-5-5"); !ok {
			t.Fatal("bad disk cache replaced last good in-memory catalog")
		}
	}
}

func TestClientConcurrentRefreshAndLookup(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(t.TempDir(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return fixtureClient(fixture).Transport.RoundTrip(req)
	})})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := client.Refresh(context.Background()); err != nil {
				t.Error(err)
			}
			for range 5 {
				client.Lookup("anthropic", "claude-haiku-5-5")
				client.Snapshot()
				client.ProviderForAPI("https://api.anthropic.com/v1")
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent refresh fetched %d times", calls.Load())
	}
}

func TestClientPersistenceFailureKeepsFetchedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	client := NewClient(path, fixtureClient(fixture))
	if err := client.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "save models.dev cache") {
		t.Fatalf("persistence error = %v", err)
	}
	if _, _, ok := client.Lookup("anthropic", "claude-haiku-5-5"); !ok {
		t.Fatal("persistence failure discarded successful fetch")
	}
}

func TestProviderForAPI(t *testing.T) {
	client := NewClient("", fixtureClient(fixture))
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://api.anthropic.com/v1", "https://API.ANTHROPIC.COM/v1/"} {
		if key, ok := client.ProviderForAPI(endpoint); !ok || key != "anthropic" {
			t.Fatalf("ProviderForAPI(%q) = %q, %v", endpoint, key, ok)
		}
	}
	for _, endpoint := range []string{
		"https://api.anthropic.com", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v11",
		"http://api.anthropic.com/v1", "https://api.anthropic.com.evil/v1", "https://api.anthropic.com:8443/v1",
		"https://user:password@api.anthropic.com/v1", "https://api.anthropic.com/v1?query", "https://api.anthropic.com/v1#fragment", "", "not a URL",
	} {
		if key, ok := client.ProviderForAPI(endpoint); ok {
			t.Fatalf("ProviderForAPI accepted %q as %q", endpoint, key)
		}
	}
	client.mu.Lock()
	client.catalog["duplicate"] = Provider{API: "https://api.anthropic.com/v1/", Models: map[string]Model{}}
	client.catalog["no-api"] = Provider{Models: map[string]Model{}}
	client.mu.Unlock()
	if key, ok := client.ProviderForAPI("https://api.anthropic.com/v1"); ok || key != "" {
		t.Fatalf("ambiguous API guessed %q", key)
	}
}

func TestDiskCacheKeepsExplicitFreeAndMissingRates(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot(t, dir, time.Now().UTC(), fixture)
	client := NewClient(dir, nil)
	if err := client.Load(); err != nil {
		t.Fatal(err)
	}
	free, _, _ := client.Lookup("anthropic", "free")
	partial, _, _ := client.Lookup("anthropic", "partial")
	unknown, _, _ := client.Lookup("anthropic", "unknown")
	if free.Cost.CacheWrite == nil || *free.Cost.CacheWrite != 0 || partial.Cost.Output != nil || unknown.Cost != nil {
		t.Fatal("disk cache conflated missing and explicit zero rates")
	}
	// Assert persisted data carries a retrieval timestamp, not an expiry time.
	body, err := os.ReadFile(filepath.Join(dir, CacheFile))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot diskSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || snapshot.FetchedAt.IsZero() {
		t.Fatalf("source timestamp missing: %v", err)
	}
}

func TestClientRejectsFutureSnapshotAndRefreshRecovers(t *testing.T) {
	dir := t.TempDir()
	future := time.Now().UTC().Add(24 * time.Hour)
	writeSnapshot(t, dir, future, fixture)
	client := NewClient(dir, fixtureClient(fixture))
	if err := client.Load(); err == nil {
		t.Fatal("accepted future retrieval timestamp")
	}
	if _, _, ok := client.Lookup("anthropic", "claude-haiku-5-5"); ok {
		t.Fatal("future snapshot became visible")
	}
	before := time.Now()
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, at, ok := client.Lookup("anthropic", "claude-haiku-5-5"); !ok || at.Before(before) || at.After(time.Now()) {
		t.Fatalf("fresh retrieval did not replace future snapshot: %v %v", at, ok)
	}
	// A future disk snapshot must not roll back an existing good snapshot or
	// pin the install timestamp so later successful fetches are ignored.
	_, goodAt, _ := client.Lookup("anthropic", "claude-haiku-5-5")
	writeSnapshot(t, dir, future, fixture)
	if err := client.Load(); err == nil {
		t.Fatal("accepted future retrieval timestamp over last good snapshot")
	}
	if _, at, ok := client.Lookup("anthropic", "claude-haiku-5-5"); !ok || !at.Equal(goodAt) {
		t.Fatal("future timestamp replaced the last good snapshot")
	}
}
