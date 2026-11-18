package proxy

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/filelock"
	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const (
	debugMetadataLimit int64 = 32 << 10
	debugRecordLimit         = 1000
)

// Payload debugging is a separate, explicitly private archive, never a ledger
// column. Inspection is restricted to authenticated, bounded JSON previews.
// Limits include existing files across restarts.
type payloadDebugger struct {
	mu                   sync.Mutex
	root                 string
	rootInfo             os.FileInfo
	enabled              bool
	total                int64
	perStream, remaining int64
	records              int
	warned, closed       bool
	active               map[string]*payloadCapture
	retained             map[string]retainedDebugCapture
	unlock               func() error
	closeOnce            sync.Once
	closeErr             error
}

type capturedStream struct {
	BytesSeen    int64 `json:"bytes_seen"`
	BytesStored  int64 `json:"bytes_stored"`
	EOF          bool  `json:"eof"`
	Truncated    bool  `json:"truncated"`
	ReadError    bool  `json:"read_error"`
	WriteError   bool  `json:"write_error"`
	CaptureError bool  `json:"capture_error"`
	file         *os.File
}

type payloadManifest struct {
	ID                  string                     `json:"id"`
	Route               string                     `json:"route"`
	Method              string                     `json:"method"`
	LocalPath           string                     `json:"local_path"`
	Started             int64                      `json:"started"`
	Ended               int64                      `json:"ended,omitempty"`
	Status              int                        `json:"status,omitempty"`
	Outcome             string                     `json:"outcome"`
	Model               string                     `json:"model,omitempty"`
	Usage               *ObservedUsage             `json:"usage,omitempty"`
	RequestContentType  string                     `json:"request_content_type,omitempty"`
	ResponseContentType string                     `json:"response_content_type,omitempty"`
	ResponseEncoding    string                     `json:"response_encoding,omitempty"`
	WebSocket           bool                       `json:"websocket"`
	Finished            bool                       `json:"finished"`
	Streams             map[string]*capturedStream `json:"streams"`
}

type payloadCapture struct {
	mu       sync.Mutex
	owner    *payloadDebugger
	dir      string
	root     *os.Root
	meta     *os.File
	manifest payloadManifest
	charged  int64
	finished bool
}

func openPayloadDebugger(opts Options) (_ *payloadDebugger, err error) {
	if opts.DebugPayloadDir == "" {
		return nil, nil
	}
	perStream, total, err := debugPayloadLimits(opts)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(opts.DebugPayloadDir)
	if err != nil {
		return nil, fmt.Errorf("resolve debug payload directory: %w", err)
	}
	if err := fileprivacy.MkdirAll(root); err != nil {
		return nil, fmt.Errorf("create debug payload directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || fileprivacy.CheckDir(info) != nil {
		return nil, fmt.Errorf("debug payload directory must be a private non-symlink directory (0700)")
	}
	lockPath := filepath.Join(root, ".capture.lock")
	if st, e := os.Lstat(lockPath); e == nil {
		if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || fileprivacy.CheckFile(st) != nil {
			return nil, fmt.Errorf("debug payload lock must be a private regular non-symlink file")
		}
	} else if !os.IsNotExist(e) {
		return nil, fmt.Errorf("inspect debug payload lock: %w", e)
	}
	unlock, err := filelock.TryLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock debug payload directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = unlock()
		}
	}()
	archiveSizes := make(map[string]int64)
	used, records, err := scanDebugArchive(root, archiveSizes)
	if err != nil {
		return nil, err
	}
	d := &payloadDebugger{root: root, rootInfo: info, enabled: true, total: total, perStream: perStream, remaining: total - used, records: records, active: map[string]*payloadCapture{}, unlock: unlock}
	if err := d.indexRetainedCaptures(archiveSizes); err != nil {
		return nil, err
	}
	return d, nil
}

func scanDebugArchive(root string, byCapture map[string]int64) (used int64, records int, err error) {
	entries := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 10000 {
			return fmt.Errorf("debug payload archive has too many entries; choose a fresh directory")
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("debug payload archive must not contain symlinks")
		}
		if entry.IsDir() {
			if fileprivacy.CheckDir(info) != nil {
				return fmt.Errorf("debug payload archive contains a non-private directory")
			}
			if path != root && filepath.Dir(path) == root {
				records++
			}
		} else {
			if !info.Mode().IsRegular() || fileprivacy.CheckFile(info) != nil {
				return fmt.Errorf("debug payload archive must contain only private regular files")
			}
			if info.Size() > (1<<63-1)-used {
				return fmt.Errorf("debug payload archive size overflow")
			}
			used += info.Size()
			if parent := filepath.Dir(path); byCapture != nil && filepath.Dir(parent) == root && parent != root {
				byCapture[filepath.Base(parent)] += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		err = fmt.Errorf("inspect debug payload archive: %w", err)
	}
	return
}

// caller holds d.mu. One warning, with no body, query, header or credential data.
func (d *payloadDebugger) warn(reason string) {
	if !d.warned {
		log.Printf("proxy: debug payload capture limited: %s", reason)
		d.warned = true
	}
}
func (d *payloadDebugger) reserve(n int64) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.enabled {
		return 0
	}
	if n > d.remaining && len(d.retained) != 0 {
		if root, err := d.openRoot(); err == nil {
			d.makeRoom(root, n, false)
			_ = root.Close()
		}
	}
	if n > d.remaining {
		n = max(0, d.remaining)
		d.warn("archive byte limit reached; no inactive capture could free enough space")
	}
	d.remaining -= n
	return n
}

func (d *payloadDebugger) begin(rec Record, req *http.Request) *payloadCapture {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.enabled {
		return nil
	}
	// IDs are generated internally. Refuse path-like input even in test callers.
	if !slug.MatchString(rec.ID) || d.active[rec.ID] != nil {
		return nil
	}
	archive, err := d.openRoot()
	if err != nil {
		d.warn("unsafe archive directory")
		return nil
	}
	defer archive.Close()
	if !d.hasRoom(debugMetadataLimit, true) {
		if _, err := archive.Lstat(rec.ID); !os.IsNotExist(err) {
			return nil
		}
		d.makeRoom(archive, debugMetadataLimit, true)
		if !d.hasRoom(debugMetadataLimit, true) {
			d.warn("archive record/byte limit reached; no inactive capture could free enough space")
			return nil
		}
	}
	dir := filepath.Join(d.root, rec.ID)
	if err := archive.Mkdir(rec.ID, 0700); err != nil {
		d.warn("cannot create request directory")
		return nil
	}
	record, err := openDebugRecord(archive, rec.ID)
	if err != nil {
		d.warn("unsafe request directory")
		return nil
	}
	meta, err := record.OpenFile("manifest.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = record.Close()
		_ = archive.Remove(rec.ID)
		d.warn("cannot create request manifest")
		return nil
	}
	d.remaining -= debugMetadataLimit // reserve metadata space before bodies can consume the quota
	d.records++
	c := &payloadCapture{owner: d, dir: dir, root: record, meta: meta, charged: debugMetadataLimit, manifest: payloadManifest{ID: rec.ID, Route: rec.Route, Method: rec.Method, LocalPath: rec.LocalPrefix + strings.TrimPrefix(rec.Path, "/"), Started: rec.Started, Outcome: "active", RequestContentType: debugHeader(req.Header.Get("Content-Type"), 256), Streams: map[string]*capturedStream{}}}
	if err := c.writeManifest(); err != nil {
		_ = meta.Close()
		_ = record.Close()
		d.warn("cannot write request manifest")
		return nil
	}
	d.active[rec.ID] = c
	return c
}

func debugHeader(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// Called under c.mu, or before publishing a new capture. A partial manifest after
// a crash is deliberately not evidence of completed transport.
func (c *payloadCapture) writeManifest() error {
	data, err := json.MarshalIndent(c.manifest, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data)) > debugMetadataLimit {
		return fmt.Errorf("debug metadata limit exceeded")
	}
	if _, err := c.meta.Seek(0, 0); err != nil {
		return err
	}
	if err := c.meta.Truncate(0); err != nil {
		return err
	}
	n, err := c.meta.Write(data)
	if err == nil && n != len(data) {
		err = fmt.Errorf("short debug metadata write")
	}
	return err
}

func (c *payloadCapture) observe(name string, data []byte, eof, readError, writeError bool) {
	if c == nil || !allowedDebugFile(name) || name == "manifest.json" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	stream := c.manifest.Streams[name]
	if stream == nil {
		stream = &capturedStream{}
		c.manifest.Streams[name] = stream
	}
	stream.BytesSeen += int64(len(data))
	stream.EOF = stream.EOF || eof
	stream.ReadError = stream.ReadError || readError
	stream.WriteError = stream.WriteError || writeError
	if len(data) == 0 || stream.CaptureError {
		return
	}
	count := min(int64(len(data)), c.owner.perStream-stream.BytesStored)
	if count > 0 {
		count = c.owner.reserve(count)
		c.charged += count
	}
	if count < int64(len(data)) {
		stream.Truncated = true
	}
	if count == 0 {
		return
	}
	if stream.file == nil {
		file, err := c.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			stream.CaptureError = true
			return
		}
		stream.file = file
	}
	n, err := stream.file.Write(data[:count])
	stream.BytesStored += int64(n)
	if err != nil || int64(n) != count {
		stream.CaptureError = true
	}
}

func (c *payloadCapture) response(resp *http.Response) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	c.manifest.ResponseContentType = debugHeader(resp.Header.Get("Content-Type"), 256)
	c.manifest.ResponseEncoding = debugHeader(resp.Header.Get("Content-Encoding"), 64)
	c.manifest.WebSocket = resp.StatusCode == http.StatusSwitchingProtocols
	c.manifest.Status = resp.StatusCode
}

func (c *payloadCapture) finish(rec Record) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	// Linearize finalization with disable/shutdown. A transport completing
	// concurrently must not overwrite the reason capture was stopped.
	c.owner.mu.Lock()
	if c.owner.closed {
		rec.Ended, rec.Outcome = time.Now().UnixMilli(), "debug_capture_shutdown"
	} else if !c.owner.enabled {
		rec.Ended, rec.Outcome = time.Now().UnixMilli(), "debug_capture_disabled"
	}
	c.finished = true
	c.owner.mu.Unlock()
	c.manifest.Ended, c.manifest.Outcome = rec.Ended, rec.Outcome
	if rec.Status != 0 {
		c.manifest.Status = rec.Status
	}
	c.manifest.Finished = true
	c.manifest.Usage = rec.Usage
	if rec.Usage != nil {
		c.manifest.Model = rec.Usage.Usage.Model
	}
	for _, stream := range c.manifest.Streams {
		if stream.file != nil && stream.file.Close() != nil {
			stream.CaptureError = true
		}
	}
	err := c.writeManifest()
	info, statErr := c.root.Stat(".")
	closeErr := c.meta.Close()
	rootErr := c.root.Close()
	retained := retainedDebugCapture{id: c.manifest.ID, started: c.manifest.Started, charged: c.charged, info: info}
	failed := err != nil || closeErr != nil || rootErr != nil
	for _, stream := range c.manifest.Streams {
		failed = failed || stream.CaptureError
	}
	c.mu.Unlock()
	c.owner.mu.Lock()
	if failed {
		c.owner.warn("capture file write/close failed; forwarding was not interrupted")
	}
	delete(c.owner.active, c.manifest.ID)
	if err == nil && statErr == nil {
		c.owner.retained[retained.id] = retained
	}
	c.owner.mu.Unlock()
}

func (d *payloadDebugger) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		active := make([]*payloadCapture, 0, len(d.active))
		for _, c := range d.active {
			active = append(active, c)
		}
		d.mu.Unlock()
		for _, c := range active {
			c.finish(Record{Ended: time.Now().UnixMilli(), Outcome: "debug_capture_shutdown"})
		}
		d.closeErr = d.unlock()
	})
	return d.closeErr
}
