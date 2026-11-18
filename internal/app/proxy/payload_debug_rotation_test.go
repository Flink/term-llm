package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func rotationCapture(t *testing.T, d *payloadDebugger, id string, started int64) *payloadCapture {
	t.Helper()
	rec, req := debugRequest(id)
	rec.Started = started
	c := d.begin(rec, req)
	if c == nil {
		t.Fatalf("begin(%q) did not admit capture", id)
	}
	return c
}

func finishRotationCapture(c *payloadCapture) {
	rec := Record{ID: c.manifest.ID, Ended: c.manifest.Started + 1, Outcome: "complete"}
	c.finish(rec)
}

func requireCapturePresent(t *testing.T, root, id string, present bool) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, id))
	if present && err != nil {
		t.Fatalf("capture %s should remain: %v", id, err)
	}
	if !present && !os.IsNotExist(err) {
		t.Fatalf("capture %s should be evicted, stat error=%v", id, err)
	}
}

func TestPayloadDebugRotationEvictsOldestInactiveAtAdmission(t *testing.T) {
	// Two metadata reservations fit; the third admission needs to evict the
	// oldest inactive record while preserving the active one.
	d := debugForTest(t, 16, 2*debugMetadataLimit+64)
	old := rotationCapture(t, d, "px_old", 1)
	finishRotationCapture(old)
	active := rotationCapture(t, d, "px_active", 3)
	newest := rotationCapture(t, d, "px_newest", 5)
	requireCapturePresent(t, d.root, "px_old", false)
	requireCapturePresent(t, d.root, "px_active", true)
	requireCapturePresent(t, d.root, "px_newest", true)
	finishRotationCapture(active)
	finishRotationCapture(newest)
}

func TestPayloadDebugRotationEvictsOldestWhenStreamWriteNeedsBytes(t *testing.T) {
	// Keep one metadata reservation in reserve and only eight body bytes. The
	// second write must reclaim the first completed capture's payload bytes.
	d := debugForTest(t, 64, 2*debugMetadataLimit+8)
	old := rotationCapture(t, d, "px_old_bytes", 1)
	old.observe("request.body", []byte("12345678"), true, false, false)
	finishRotationCapture(old)
	newer := rotationCapture(t, d, "px_new_bytes", 2)
	newer.observe("request.body", []byte("abcdefgh"), true, false, false)
	finishRotationCapture(newer)
	m := debugManifest(t, newer)
	if s := m.Streams["request.body"]; s == nil || s.BytesStored != 8 {
		t.Fatalf("stream was not fully written after eviction: %+v", m.Streams)
	}
	requireCapturePresent(t, d.root, "px_old_bytes", false)
	requireCapturePresent(t, d.root, "px_new_bytes", true)
}

func TestPayloadDebugRotationRestartAccountsAndEvictsOlderCaptures(t *testing.T) {
	d := debugForTest(t, 32, 3*debugMetadataLimit+32)
	old := rotationCapture(t, d, "px_restart_old", 10)
	old.observe("request.body", []byte("old-data"), true, false, false)
	finishRotationCapture(old)
	newer := rotationCapture(t, d, "px_restart_new", 20)
	finishRotationCapture(newer)
	root := d.root
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	used, records, err := scanDebugArchive(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Reopening accounts actual on-disk file sizes, not the in-memory metadata
	// reservation. Leave one byte less than a new metadata reservation so the
	// oldest record is removed to admit the next request.
	total := used + debugMetadataLimit - 1
	reopened, err := openPayloadDebugger(Options{DebugPayloadDir: root, DebugPayloadMaxBytes: 32, DebugPayloadMaxTotalBytes: total})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.records != records || reopened.remaining != total-used {
		t.Fatalf("restart did not account for existing captures: records=%d remaining=%d", reopened.records, reopened.remaining)
	}
	rec, req := debugRequest("px_restart_latest")
	rec.Started = 30
	latest := reopened.begin(rec, req)
	if latest == nil {
		t.Fatal("restart did not make an older capture eligible for eviction")
	}
	requireCapturePresent(t, root, "px_restart_old", false)
	requireCapturePresent(t, root, "px_restart_new", true)
	finishRotationCapture(latest)
}

func TestPayloadDebugRotationOnlyDeletesValidAllowlistedPrivateArchives(t *testing.T) {
	d := debugForTest(t, 16, 2*debugMetadataLimit+32)
	unsafe := rotationCapture(t, d, "px_unsafe", 1)
	finishRotationCapture(unsafe)
	valid := rotationCapture(t, d, "px_valid", 2)
	finishRotationCapture(valid)
	unrelated := filepath.Join(d.root, "notes.txt")
	unexpected := filepath.Join(unsafe.dir, "unexpected.txt")
	for _, path := range []string{unrelated, unexpected} {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest capture is contaminated after indexing. Rotation must skip
	// it intact and use the next eligible capture instead.
	latest := rotationCapture(t, d, "px_latest", 3)
	finishRotationCapture(latest)
	requireCapturePresent(t, d.root, "px_valid", false)
	for _, path := range []string{unrelated, unexpected, filepath.Join(unsafe.dir, "manifest.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("rotation removed an unsafe or unrelated entry: %v", err)
		}
	}
}

func TestPayloadDebugRotationDoesNotEvictActiveAtRecordCap(t *testing.T) {
	d := debugForTest(t, 1, debugMetadataLimit*int64(debugRecordLimit+2))
	active := rotationCapture(t, d, "px_cap_active", 1)
	for i := 1; i < debugRecordLimit; i++ {
		id := fmt.Sprintf("px_seed_%04d", i)
		c := rotationCapture(t, d, id, int64(i+1))
		finishRotationCapture(c)
	}
	// The record ceiling itself should prompt inactive pruning to admit one
	// more request, without ending the active capture.
	rec, req := debugRequest("px_cap_last")
	rec.Started = debugRecordLimit + 1
	last := d.begin(rec, req)
	if last == nil {
		t.Fatal("record cap was not relieved by evicting inactive captures")
	}
	if d.active[active.manifest.ID] != active {
		t.Fatal("active capture was evicted")
	}
	finishRotationCapture(last)
	finishRotationCapture(active)
}
