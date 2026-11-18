package proxy

import (
	"cmp"
	"io"
	"os"
	"slices"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

type retainedDebugCapture struct {
	id      string
	started int64
	charged int64
	info    os.FileInfo
}

// Index existing captures while the archive lock is held. Crash leftovers
// with a valid manifest are inactive now, so they can rotate too. Unrecognized
// contents still count against the archive limits, but are never deleted.
func (d *payloadDebugger) indexRetainedCaptures(sizes map[string]int64) error {
	d.retained = make(map[string]retainedDebugCapture)
	if len(sizes) == 0 {
		return nil
	}
	root, err := d.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	for id, size := range sizes {
		if !slug.MatchString(id) {
			continue
		}
		if capture, ok := indexDebugCapture(root, id); ok && capture.charged == size {
			d.retained[id] = capture
		}
	}
	return nil
}

func indexDebugCapture(root *os.Root, id string) (retainedDebugCapture, bool) {
	record, err := openDebugRecord(root, id)
	if err != nil {
		return retainedDebugCapture{}, false
	}
	defer record.Close()
	info, err := record.Stat(".")
	if err != nil {
		return retainedDebugCapture{}, false
	}
	manifest, err := readPayloadManifest(record, id)
	if err != nil {
		return retainedDebugCapture{}, false
	}
	files, err := debugRotationFiles(record)
	if err != nil {
		return retainedDebugCapture{}, false
	}
	capture := retainedDebugCapture{id: id, started: manifest.Started, info: info}
	for _, file := range files {
		if file.Size() > 1<<63-1-capture.charged {
			return retainedDebugCapture{}, false
		}
		capture.charged += file.Size()
	}
	return capture, true
}

func debugRotationFiles(record *os.Root) ([]os.FileInfo, error) {
	dir, err := record.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(len(debugFiles) + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	files := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := record.Lstat(entry.Name())
		if err != nil {
			return nil, err
		}
		if !allowedDebugFile(entry.Name()) || !info.Mode().IsRegular() || fileprivacy.CheckFile(info) != nil {
			return nil, errUnsafeDebugFile
		}
		// Delete the manifest last so partially removed captures still have an ID.
		if entry.Name() != "manifest.json" {
			files = append(files, info)
		}
	}
	manifest, err := record.Lstat("manifest.json")
	if err != nil {
		return nil, err
	}
	if !manifest.Mode().IsRegular() || fileprivacy.CheckFile(manifest) != nil {
		return nil, errUnsafeDebugFile
	}
	return append(files, manifest), nil
}

func removeRetainedDebugCapture(root *os.Root, capture retainedDebugCapture) error {
	record, err := openDebugRecord(root, capture.id)
	if err != nil {
		return err
	}
	defer record.Close()
	info, err := record.Stat(".")
	if err != nil || !os.SameFile(info, capture.info) {
		return errUnsafeDebugFile
	}
	if _, err := readPayloadManifest(record, capture.id); err != nil {
		return err
	}
	files, err := debugRotationFiles(record)
	if err != nil {
		return err
	}
	for _, file := range files {
		latest, err := record.Lstat(file.Name())
		if err != nil || !os.SameFile(file, latest) || !latest.Mode().IsRegular() || fileprivacy.CheckFile(latest) != nil {
			return errUnsafeDebugFile
		}
		if err := record.Remove(file.Name()); err != nil {
			return err
		}
	}
	latest, err := root.Lstat(capture.id)
	if err != nil || !os.SameFile(info, latest) {
		return errUnsafeDebugFile
	}
	return root.Remove(capture.id) // Nonrecursive; never remove unexpected contents.
}

// Caller holds d.mu. Only inactive captures are indexed. A failure never frees
// quota in memory, even if some files were removed before it was detected.
func (d *payloadDebugger) makeRoom(root *os.Root, bytes int64, newRecord bool) {
	if d.hasRoom(bytes, newRecord) || len(d.retained) == 0 {
		return
	}
	oldest := make([]retainedDebugCapture, 0, len(d.retained))
	for _, capture := range d.retained {
		oldest = append(oldest, capture)
	}
	slices.SortFunc(oldest, func(a, b retainedDebugCapture) int {
		if order := cmp.Compare(a.started, b.started); order != 0 {
			return order
		}
		return cmp.Compare(a.id, b.id)
	})
	for _, capture := range oldest {
		delete(d.retained, capture.id)
		if err := removeRetainedDebugCapture(root, capture); err != nil {
			d.warn("could not rotate old capture safely")
			continue
		}
		d.remaining += capture.charged
		d.records--
		if d.hasRoom(bytes, newRecord) {
			return
		}
	}
}

func (d *payloadDebugger) hasRoom(bytes int64, newRecord bool) bool {
	return d.remaining >= bytes && (!newRecord || d.records < debugRecordLimit)
}
