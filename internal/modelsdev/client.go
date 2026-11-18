package modelsdev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

const (
	TTL          = 12 * time.Hour
	CacheFile    = "models-dev-catalog.json"
	cacheVersion = 1
)

type diskSnapshot struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	Catalog   Catalog   `json:"catalog"`
}

// Client is a concurrency-safe snapshot cache. Load and Lookup never fetch.
// Refresh is intended for startup/background work, not request-time lookups.
// A stale snapshot remains usable when a fetch or cache load fails.
type Client struct {
	// Endpoint is an optional explicit catalog URL. Set it before using the
	// client concurrently; empty uses the configured models.dev endpoint.
	Endpoint string

	cacheDir   string
	httpClient *http.Client
	refreshMu  sync.Mutex
	mu         sync.RWMutex
	catalog    Catalog
	fetchedAt  time.Time
}

// NewClient constructs a client without performing disk or network I/O. An
// empty cacheDir disables persistence. A nil HTTP client gets a bounded timeout.
func NewClient(cacheDir string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cacheDir: cacheDir, httpClient: client}
}

// Load installs a valid disk snapshot even if stale. Invalid/missing snapshots
// return an error without replacing the last good in-memory catalog.
func (c *Client) Load() error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.load()
}

func (c *Client) load() error {
	if c.cacheDir == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(c.cacheDir, CacheFile))
	if err != nil {
		return fmt.Errorf("open models.dev cache: %w", err)
	}
	defer file.Close()
	body, err := providerhttp.ReadBounded(file, MaxBodyBytes)
	if err != nil {
		return fmt.Errorf("read models.dev cache: %w", err)
	}
	var cached diskSnapshot
	if err := json.Unmarshal(body, &cached); err != nil {
		return fmt.Errorf("decode models.dev cache: %w", err)
	}
	if cached.Version != cacheVersion || cached.FetchedAt.IsZero() || cached.FetchedAt.After(time.Now()) {
		return fmt.Errorf("invalid models.dev cache version or retrieval time")
	}
	if err := validate(cached.Catalog); err != nil {
		return fmt.Errorf("invalid models.dev cache: %w", err)
	}
	if !hasModels(cached.Catalog) {
		return fmt.Errorf("models.dev cache carried no models")
	}
	c.install(cached.Catalog, cached.FetchedAt)
	return nil
}

// Refresh loads the disk cache if needed and fetches only when the source
// retrieval time is older than TTL. File modification times do not extend TTL.
// A successful fetch is usable even if persistence fails (the error is returned).
func (c *Client) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	loaded := c.catalog != nil
	c.mu.RUnlock()
	if !loaded {
		_ = c.load()
	}
	c.mu.RLock()
	fresh := !c.fetchedAt.IsZero() && time.Since(c.fetchedAt) >= 0 && time.Since(c.fetchedAt) < TTL
	c.mu.RUnlock()
	if fresh {
		return nil
	}
	catalog, err := Fetch(ctx, c.httpClient, c.Endpoint)
	if err != nil {
		return err
	}
	if !hasModels(catalog) {
		return fmt.Errorf("models.dev catalog carried no models")
	}
	fetchedAt := time.Now().UTC()
	c.install(catalog, fetchedAt)
	if c.cacheDir == "" {
		return nil
	}
	if err := c.save(catalog, fetchedAt); err != nil {
		return fmt.Errorf("save models.dev cache: %w", err)
	}
	return nil
}

func hasModels(catalog Catalog) bool {
	for _, provider := range catalog {
		if len(provider.Models) > 0 {
			return true
		}
	}
	return false
}

func (c *Client) install(catalog Catalog, fetchedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// An explicit Load must not roll a newer successful fetch back.
	if c.catalog == nil || !fetchedAt.Before(c.fetchedAt) {
		c.catalog, c.fetchedAt = catalog, fetchedAt
	}
}

// Lookup returns an exact provider/model match, source retrieval timestamp and
// presence flag. Returned pointers may be changed without modifying the cache.
func (c *Client) Lookup(provider, model string) (Model, time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.catalog[provider].Models[model]
	return cloneModel(entry), c.fetchedAt, ok
}

// Snapshot returns a deep copy for consumers that build their own projections
// or match a provider by its published API endpoint.
func (c *Client) Snapshot() (Catalog, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneCatalog(c.catalog), c.fetchedAt
}

func (c *Client) save(catalog Catalog, fetchedAt time.Time) error {
	body, err := json.Marshal(diskSnapshot{Version: cacheVersion, FetchedAt: fetchedAt, Catalog: catalog})
	if err != nil {
		return err
	}
	if int64(len(body)) > MaxBodyBytes {
		return fmt.Errorf("snapshot exceeds %d byte limit", MaxBodyBytes)
	}
	if err := os.MkdirAll(c.cacheDir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(c.cacheDir, ".models-dev-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(c.cacheDir, CacheFile))
}
