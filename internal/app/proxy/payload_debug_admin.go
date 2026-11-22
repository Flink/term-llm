package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/term-llm/term-llm/internal/fileprivacy"

	"github.com/term-llm/term-llm/internal/app/proxy/debugheaders"
	"github.com/term-llm/term-llm/internal/app/proxy/debugws"
)

var errInvalidDebugManifest = errors.New("capture manifest is incomplete or exceeds size limit")

var errUnsafeDebugFile = errors.New("debug archive must contain private non-symlink directories and regular files")
var debugFiles = []string{"manifest.json", "request.body", "response.body", "websocket-client.frames", "websocket-server.frames", debugheaders.Request, debugheaders.UpstreamRequest, debugheaders.UpstreamResponse}

func allowedDebugFile(name string) bool {
	for _, file := range debugFiles {
		if file == name {
			return true
		}
	}
	return false
}

func (s *Service) payloadDebugAPI(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "debug" && !strings.HasPrefix(path, "debug/") {
		return false
	}
	if path == "debug" {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			var in struct {
				Enabled              *bool `json:"enabled"`
				AcknowledgeSensitive bool  `json:"acknowledge_sensitive"`
			}
			if !readJSON(w, r, &in) {
				return true
			}
			if in.Enabled == nil || (*in.Enabled && !in.AcknowledgeSensitive) {
				fail(w, 400, "Enabling capture requires enabled and acknowledge_sensitive: true; bodies may contain secrets")
				return true
			}
			if err := s.setPayloadDebugEnabled(*in.Enabled); err != nil {
				debugAPIFail(w, err)
				return true
			}
		default:
			fail(w, 405, "Method not allowed")
			return true
		}
		status, err := s.payloadDebugStatus()
		if err != nil {
			debugAPIFail(w, err)
		} else {
			jsonReply(w, 200, status)
		}
		return true
	}
	if r.Method != http.MethodGet {
		fail(w, 405, "Method not allowed")
		return true
	}
	s.debugMu.Lock()
	defer s.debugMu.Unlock()
	if s.debugClosing {
		fail(w, 409, "Proxy is shutting down")
		return true
	}
	if path == "debug/captures" {
		if err := s.initPayloadDebug(false); err != nil {
			debugAPIFail(w, err)
			return true
		}
		limit, err := debugQueryInt(r, "limit", 50, 1, 100)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		captures, err := s.listPayloadCaptures(int(limit))
		if err != nil {
			debugAPIFail(w, err)
		} else {
			jsonReply(w, 200, map[string]any{"captures": captures, "limit": limit})
		}
		return true
	}
	id, file, preview := strings.Cut(strings.TrimPrefix(path, "debug/captures/"), "/")
	if !strings.HasPrefix(path, "debug/captures/") || !slug.MatchString(id) || (preview && !allowedDebugFile(file)) {
		fail(w, 400, "Invalid capture ID or file; only capture manifests and body/frame files are available")
		return true
	}
	if !preview {
		s.describePayloadCapture(w, id)
		return true
	}
	offset, err := debugQueryInt(r, "offset", 0, 0, 4<<30)
	if err != nil {
		fail(w, 400, err.Error())
		return true
	}
	limit, err := debugQueryInt(r, "limit", debugPreviewLimit, 1, debugPreviewLimit)
	if err != nil {
		fail(w, 400, err.Error())
		return true
	}
	view := r.URL.Query()["view"]
	decoded := len(view) == 1 && view[0] == "decoded"
	if len(view) != 0 && (!decoded || (file != "websocket-client.frames" && file != "websocket-server.frames")) {
		fail(w, 400, "Decoded view is only available for WebSocket frame files")
		return true
	}
	s.previewPayloadCapture(w, id, file, offset, limit, decoded)
	return true
}

func debugQueryInt(r *http.Request, key string, fallback, minValue, maxValue int64) (int64, error) {
	values, ok := r.URL.Query()[key]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("Invalid %s", key)
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minValue, maxValue)
	}
	return value, nil
}

func debugAPIFail(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errInvalidDebugManifest) {
		fail(w, 404, "Capture or file not found")
		return
	}
	fail(w, 409, "Debug archive unavailable or unsafe; require private non-symlink capture directories and regular files")
}

// Root-relative operations prevent escaping the archive even if an operator
// concurrently replaces a path. Identity checks reject symlinks, including
// aliases that remain inside the root, before any payload bytes are read.
func openPrivateDebugRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || fileprivacy.CheckDir(before) != nil {
		return nil, errUnsafeDebugFile
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) || fileprivacy.CheckDir(after) != nil {
		root.Close()
		return nil, errUnsafeDebugFile
	}
	return root, nil
}

func openDebugRecord(root *os.Root, id string) (*os.Root, error) {
	before, err := root.Lstat(id)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || fileprivacy.CheckDir(before) != nil {
		return nil, errUnsafeDebugFile
	}
	record, err := root.OpenRoot(id)
	if err != nil {
		return nil, err
	}
	after, err := record.Stat(".")
	if err != nil || !os.SameFile(before, after) || fileprivacy.CheckDir(after) != nil {
		record.Close()
		return nil, errUnsafeDebugFile
	}
	return record, nil
}

func openDebugFile(record *os.Root, name string) (*os.File, error) {
	before, err := record.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || fileprivacy.CheckFile(before) != nil {
		return nil, errUnsafeDebugFile
	}
	f, err := record.Open(name)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	latest, lerr := record.Lstat(name)
	if err != nil || lerr != nil || !os.SameFile(before, after) || !os.SameFile(before, latest) || latest.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || fileprivacy.CheckFile(after) != nil {
		f.Close()
		return nil, errUnsafeDebugFile
	}
	return f, nil
}

// Called under debugMu. Synchronize manifest reads with finalization; payload
// file previews themselves may show an actively growing prefix.
func (s *Service) lockDebugCapture(id string) func() {
	if d := s.payloadDebug; d != nil {
		d.mu.Lock()
		active := d.active[id]
		d.mu.Unlock()
		if active != nil {
			active.mu.Lock()
			return active.mu.Unlock
		}
	}
	return func() {}
}

type payloadCaptureSummary struct {
	ID        string   `json:"id"`
	Started   int64    `json:"started"`
	Ended     int64    `json:"ended,omitempty"`
	Route     string   `json:"route"`
	Method    string   `json:"method"`
	LocalPath string   `json:"local_path"`
	Outcome   string   `json:"outcome"`
	Status    int      `json:"status,omitempty"`
	Finished  bool     `json:"finished"`
	WebSocket bool     `json:"websocket"`
	Files     []string `json:"files"`
}

func (s *Service) listPayloadCaptures(limit int) ([]payloadCaptureSummary, error) {
	captures := []payloadCaptureSummary{}
	if s.payloadDebug == nil {
		return captures, nil
	}
	root, err := s.payloadDebug.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// Includes the single lock file, plus a bounded overflow sentinel.
	entries, err := dir.ReadDir(debugRecordLimit + 2)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > debugRecordLimit+1 {
		return nil, fmt.Errorf("debug archive exceeds record limit")
	}
	for _, entry := range entries {
		if entry.Name() == ".capture.lock" {
			f, err := openDebugFile(root, entry.Name())
			if err != nil {
				return nil, err
			}
			f.Close()
			continue
		}
		if !slug.MatchString(entry.Name()) {
			return nil, errUnsafeDebugFile
		}
		summary, err := s.payloadCaptureSummary(root, entry.Name())
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errInvalidDebugManifest) {
			continue
		}
		if err != nil {
			return nil, err
		}
		captures = append(captures, summary)
	}
	sort.Slice(captures, func(i, j int) bool {
		if captures[i].Started == captures[j].Started {
			return captures[i].ID > captures[j].ID
		}
		return captures[i].Started > captures[j].Started
	})
	if len(captures) > limit {
		captures = captures[:limit]
	}
	return captures, nil
}

func (s *Service) payloadCaptureSummary(root *os.Root, id string) (payloadCaptureSummary, error) {
	var summary payloadCaptureSummary
	unlock := s.lockDebugCapture(id)
	defer unlock()
	record, err := openDebugRecord(root, id)
	if err != nil {
		return summary, err
	}
	defer record.Close()
	manifest, err := readPayloadManifest(record, id)
	if err != nil {
		return summary, err
	}
	summary = payloadCaptureSummary{ID: id, Started: manifest.Started, Ended: manifest.Ended, Route: manifest.Route, Method: manifest.Method, LocalPath: manifest.LocalPath, Outcome: manifest.Outcome, Status: manifest.Status, Finished: manifest.Finished, WebSocket: manifest.WebSocket, Files: []string{}}
	summary.Files, err = payloadCaptureFiles(record, false)
	return summary, err
}

func readPayloadManifest(record *os.Root, id string) (payloadManifest, error) {
	var manifest payloadManifest
	f, err := openDebugFile(record, "manifest.json")
	if err != nil {
		return manifest, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, debugMetadataLimit+1))
	if err != nil {
		return manifest, err
	}
	if len(data) > int(debugMetadataLimit) || json.Unmarshal(data, &manifest) != nil || manifest.ID != id {
		return manifest, errInvalidDebugManifest
	}
	return manifest, nil
}

func payloadCaptureFiles(record *os.Root, payloadOnly bool) ([]string, error) {
	files := []string{}
	for _, name := range debugFiles {
		if payloadOnly && name == "manifest.json" {
			continue
		}
		f, err := openDebugFile(record, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !payloadOnly {
			f.Close()
			files = append(files, name)
			continue
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			return nil, err
		}
		if info.Size() > 0 {
			files = append(files, name)
		}
	}
	return files, nil
}

// Open just the requested record, without initializing capture, scanning the
// archive for quota accounting, or creating a lock file on a read.
func (s *Service) openExactPayloadRecord(id string) (*os.Root, error) {
	if d := s.payloadDebug; d != nil {
		root, err := d.openRoot()
		if err != nil {
			return nil, err
		}
		defer root.Close()
		return openDebugRecord(root, id)
	}
	root, err := openPrivateDebugRoot(s.payloadDebugDirectory())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if lock, err := openDebugFile(root, ".capture.lock"); err == nil {
		lock.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return openDebugRecord(root, id)
}

func (s *Service) describePayloadCapture(w http.ResponseWriter, id string) {
	unlock := s.lockDebugCapture(id)
	defer unlock()
	record, err := s.openExactPayloadRecord(id)
	if err != nil {
		debugAPIFail(w, err)
		return
	}
	defer record.Close()
	manifest, err := readPayloadManifest(record, id)
	if err != nil {
		debugAPIFail(w, err)
		return
	}
	files, err := payloadCaptureFiles(record, true)
	if err != nil {
		debugAPIFail(w, err)
		return
	}
	jsonReply(w, 200, map[string]any{"id": id, "finished": manifest.Finished, "files": files})
}

func (s *Service) previewPayloadCapture(w http.ResponseWriter, id, name string, offset, limit int64, decoded bool) {
	unlock := s.lockDebugCapture(id)
	defer unlock()
	record, err := s.openExactPayloadRecord(id)
	if err != nil {
		debugAPIFail(w, err)
		return
	}
	defer record.Close()
	if _, err := readPayloadManifest(record, id); err != nil {
		debugAPIFail(w, err)
		return
	}
	f, err := openDebugFile(record, name)
	if err != nil {
		debugAPIFail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		debugAPIFail(w, err)
		return
	}
	total := info.Size()
	if offset > total {
		fail(w, 400, "offset exceeds file size")
		return
	}
	if decoded {
		content, next := debugws.Preview(f, offset, total, limit, name == "websocket-client.frames")
		result := map[string]any{"id": id, "file": name, "encoding": "websocket", "content": content, "total_bytes": total, "bytes_returned": next - offset, "offset": offset, "truncated": offset > 0 || next < total}
		if next > offset && next < total {
			result["next_offset"] = next
		}
		jsonReply(w, 200, result)
		return
	}
	data := make([]byte, min(limit, total-offset))
	n, err := f.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		debugAPIFail(w, err)
		return
	}
	data = data[:n]
	encoding, content := "utf8", string(data)
	if !utf8.Valid(data) {
		encoding, content = "base64", base64.StdEncoding.EncodeToString(data)
	}
	jsonReply(w, 200, map[string]any{"id": id, "file": name, "encoding": encoding, "content": content, "total_bytes": total, "bytes_returned": n, "offset": offset, "truncated": offset > 0 || offset+int64(n) < total})
}
