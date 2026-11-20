package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

func enableDebugAdmin(t *testing.T, s *Service, cookie *http.Cookie, csrf string) payloadDebugStatus {
	t.Helper()
	out := adminRequest(s, "POST", "debug", map[string]bool{"enabled": true, "acknowledge_sensitive": true}, cookie, csrf)
	if out.Code != 200 {
		t.Fatalf("enable: %d %s", out.Code, out.Body.String())
	}
	var status payloadDebugStatus
	if err := json.Unmarshal(out.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestPayloadDebugAdminSecurityAndAcknowledgment(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	for _, path := range []string{"debug", "debug/captures", "debug/captures/px_test", "debug/captures/px_test/manifest.json"} {
		out := adminRequest(s, "GET", path, nil, nil, "")
		if out.Code != 401 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("public access %s: %d", path, out.Code)
		}
	}
	for _, tt := range []struct {
		name, origin, csrf string
		cookie             *http.Cookie
		body               any
		code               int
	}{
		{"no session", "http://localhost", csrf, nil, map[string]bool{"enabled": true, "acknowledge_sensitive": true}, 401},
		{"no csrf", "http://localhost", "", cookie, map[string]bool{"enabled": true, "acknowledge_sensitive": true}, 403},
		{"no origin", "", csrf, cookie, map[string]bool{"enabled": true, "acknowledge_sensitive": true}, 403},
		{"foreign origin", "https://evil.test", csrf, cookie, map[string]bool{"enabled": true, "acknowledge_sensitive": true}, 403},
		{"no acknowledgment", "http://localhost", csrf, cookie, map[string]bool{"enabled": true}, 400},
		{"false acknowledgment", "http://localhost", csrf, cookie, map[string]bool{"enabled": true, "acknowledge_sensitive": false}, 400},
		{"no enabled", "http://localhost", csrf, cookie, map[string]bool{"acknowledge_sensitive": true}, 400},
		{"arbitrary path", "http://localhost", csrf, cookie, map[string]any{"enabled": true, "acknowledge_sensitive": true, "directory": "/tmp/forbidden"}, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "http://localhost/_proxy/api/debug", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("X-CSRF-Token", tt.csrf)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			out := httptest.NewRecorder()
			s.ServeHTTP(out, req)
			if out.Code != tt.code {
				t.Fatalf("got %d: %s", out.Code, out.Body.String())
			}
		})
	}
	if _, err := os.Lstat(s.payloadDebugDirectory()); !os.IsNotExist(err) {
		t.Fatal("rejected enable created archive", err)
	}
	out := adminRequest(s, "GET", "debug", nil, cookie, csrf)
	var before payloadDebugStatus
	if err := json.Unmarshal(out.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.Enabled || before.Directory != filepath.Join(s.opts.StateDir, "debug-payloads") || before.RecordLimit != 1000 || before.PreviewLimitBytes != 65536 {
		t.Fatal(before)
	}
	if _, err := os.Lstat(before.Directory); !os.IsNotExist(err) {
		t.Fatal("status created archive", err)
	}
	status := enableDebugAdmin(t, s, cookie, csrf)
	if !status.Enabled || s.opts.DebugPayloadDir != "" {
		t.Fatal("default path mutated CLI options", status)
	}
	out = adminRequest(s, "POST", "debug", map[string]bool{"enabled": false}, cookie, csrf)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"enabled":false`) {
		t.Fatal(out.Code, out.Body.String())
	}
}

func TestPayloadDebugAdminPreviewsAndList(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	enableDebugAdmin(t, s, cookie, csrf)
	var newest *payloadCapture
	for i, id := range []string{"px_old", "px_new"} {
		rec, req := debugRequest(id)
		rec.Started = int64(i + 1)
		c := s.beginPayloadCapture(rec, req)
		c.observe("request.body", []byte(strings.Repeat("a", debugPreviewLimit+20)), true, false, false)
		c.observe("response.body", []byte{0xff, 0, 0xfe}, true, false, false)
		c.finish(Record{Ended: 3, Outcome: "complete"})
		newest = c
	}
	out := adminRequest(s, "GET", "debug/captures?limit=1", nil, cookie, csrf)
	var list struct {
		Captures []payloadCaptureSummary `json:"captures"`
		Limit    int                     `json:"limit"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || len(list.Captures) != 1 || list.Captures[0].ID != "px_new" || list.Limit != 1 || len(list.Captures[0].Files) != 3 {
		t.Fatal(out.Code, out.Body.String())
	}
	for _, tt := range []struct {
		file, query, encoding, content string
		truncated                      bool
		total                          int
	}{
		{"request.body", "", "utf8", strings.Repeat("a", debugPreviewLimit), true, debugPreviewLimit + 20},
		{"request.body", "?offset=65536&limit=20", "utf8", strings.Repeat("a", 20), true, debugPreviewLimit + 20},
		{"response.body", "", "base64", base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0xfe}), false, 3},
	} {
		out = adminRequest(s, "GET", "debug/captures/px_new/"+tt.file+tt.query, nil, cookie, csrf)
		var preview struct {
			Encoding, Content string
			Total             int `json:"total_bytes"`
			BytesReturned     int `json:"bytes_returned"`
			Truncated         bool
		}
		if err := json.Unmarshal(out.Body.Bytes(), &preview); err != nil {
			t.Fatal(err)
		}
		wantBytes := len(tt.content)
		if tt.encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(tt.content)
			if err != nil {
				t.Fatal(err)
			}
			wantBytes = len(decoded)
		}
		if preview.BytesReturned != wantBytes {
			t.Fatalf("bytes_returned=%d, want %d", preview.BytesReturned, wantBytes)
		}
		if out.Code != 200 || preview.Encoding != tt.encoding || preview.Content != tt.content || preview.Truncated != tt.truncated || preview.Total != tt.total {
			t.Fatalf("preview %s: %d %+v", tt.file, out.Code, preview)
		}
		if out.Header().Get("Cache-Control") != "no-store" || out.Header().Get("Content-Type") != "application/json" {
			t.Fatal(out.Header())
		}
	}
	if err := os.WriteFile(filepath.Join(newest.dir, "secret.txt"), []byte("not exposed"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path string
		code int
	}{
		{"debug/captures?limit=101", 400}, {"debug/captures?limit=0", 400}, {"debug/captures?limit=1&limit=2", 400},
		{"debug/captures/px_new/request.body?limit=65537", 400}, {"debug/captures/px_new/request.body?offset=-1", 400},
		{"debug/captures/px_new/request.body?offset=99999999999", 400}, {"debug/captures/px_new/request.body?offset=999999", 400},
		{"debug/captures/px_new/secret.txt", 400}, {"debug/captures/../manifest.json", 400},
		{"debug/captures/px_new/../manifest.json", 400}, {"debug/captures/px_new/%2e%2e", 400},
		{"debug/captures/px_missing/manifest.json", 404}, {"debug/captures/px_new/websocket-server.frames", 404},
	} {
		out = adminRequest(s, "GET", tt.path, nil, cookie, csrf)
		if out.Code != tt.code {
			t.Errorf("%s: %d %s", tt.path, out.Code, out.Body.String())
		}
	}
}

func TestPayloadDebugAdminDecodedWebSocket(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	enableDebugAdmin(t, s, cookie, csrf)
	rec, req := debugRequest("px_frames")
	capture := s.beginPayloadCapture(rec, req)
	payload := []byte(`{"type":"response.create","input":"<b>plain text</b>"}`)
	key := []byte{9, 7, 5, 3}
	wire := append([]byte{0x81, 0x80 | byte(len(payload))}, key...)
	for i, b := range payload {
		wire = append(wire, b^key[i%len(key)])
	}
	wire = append(wire, 0x89, 0x80, 1, 2, 3, 4)
	capture.observe("websocket-client.frames", wire, true, false, false)
	capture.finish(Record{Ended: 3, Status: http.StatusSwitchingProtocols, Outcome: "closed_by_client"})
	path := "debug/captures/px_frames/websocket-client.frames"
	for _, tc := range []struct{ query, encoding, content string }{
		{"?view=decoded", "websocket", "response.create"},
		{"", "base64", base64.StdEncoding.EncodeToString(wire)},
	} {
		out := adminRequest(s, "GET", path+tc.query, nil, cookie, csrf)
		var got struct {
			Encoding, Content string
			Total             int `json:"total_bytes"`
			Bytes             int `json:"bytes_returned"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if out.Code != 200 || got.Encoding != tc.encoding || !strings.Contains(got.Content, tc.content) || got.Bytes != len(wire) || got.Total != len(wire) || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected frame preview: %d %+v", out.Code, got)
		}
	}
	for _, invalid := range []string{path + "?view=decoded&view=decoded", path + "?view=raw", "debug/captures/px_frames/request.body?view=decoded"} {
		if out := adminRequest(s, "GET", invalid, nil, cookie, csrf); out.Code != 400 {
			t.Fatalf("invalid view accepted: %d", out.Code)
		}
	}
	if out := adminRequest(s, "GET", path+"?view=decoded", nil, nil, ""); out.Code != 401 {
		t.Fatalf("unauthenticated preview returned %d", out.Code)
	}
}

func TestPayloadDebugAdminRejectsUnsafePaths(t *testing.T) {
	for _, kind := range []string{"archive symlink", "record symlink", "file symlink", "public file", "public record", "public archive"} {
		t.Run(kind, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			enableDebugAdmin(t, s, cookie, csrf)
			rec, req := debugRequest("px_safe")
			c := s.beginPayloadCapture(rec, req)
			c.observe("request.body", []byte("private"), true, false, false)
			c.finish(rec)
			path := filepath.Join(c.dir, "request.body")
			switch kind {
			case "archive symlink", "record symlink":
				source := c.dir
				if kind == "archive symlink" {
					source = s.payloadDebug.root
				}
				moved := source + "-moved"
				if err := os.Rename(source, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, source); err != nil {
					t.Skip(err)
				}
			case "file symlink":
				moved := path + "-moved"
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, path); err != nil {
					t.Skip(err)
				}
			default:
				if kind == "public record" {
					path = c.dir
				}
				if kind == "public archive" {
					path = s.payloadDebug.root
				}
				if err := os.Chmod(path, 0777); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if fileprivacy.CheckFile(info) == nil {
					t.Skip("platform does not enforce Unix privacy bits")
				}
			}
			for _, suffix := range []string{"", "/request.body"} {
				out := adminRequest(s, "GET", "debug/captures/px_safe"+suffix, nil, cookie, csrf)
				if out.Code != 409 || strings.Contains(out.Body.String(), path) {
					t.Fatalf("accepted %s or disclosed its path: %d %s", kind, out.Code, out.Body.String())
				}
			}
		})
	}
}

func TestPayloadDebugDefaultDirectoryProtectedFromSecrets(t *testing.T) {
	s := testService(t)
	dir := s.payloadDebugDirectory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("not a credential"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !forbiddenSecretFile(path, st, []string{dir}) {
		t.Fatal("default archive accepted as secret")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err == nil && !forbiddenSecretFile(filepath.Join(alias, "secret"), st, []string{dir}) {
		t.Fatal("archive symlink alias accepted")
	}
	config := Config{Secrets: map[string]SecretRef{"capture": {File: path}}}
	if _, err := s.resolveSecrets(config, nil, true); err == nil {
		t.Fatal("service allowed capture as credential")
	}
}

func TestPayloadDebugAdminSkipsIncompleteManifests(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	enableDebugAdmin(t, s, cookie, csrf)
	rec, req := debugRequest("px_complete")
	c := s.beginPayloadCapture(rec, req)
	c.finish(rec)
	for _, id := range []string{"px_partial", "px_oversize", "px_missing"} {
		dir := filepath.Join(s.payloadDebug.root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if id == "px_missing" {
			continue
		}
		data := []byte(`{"id":`)
		if id == "px_oversize" {
			data = bytes.Repeat([]byte("x"), int(debugMetadataLimit)+1)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := adminRequest(s, "GET", "debug/captures", nil, cookie, csrf)
	var list struct {
		Captures []payloadCaptureSummary `json:"captures"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || len(list.Captures) != 1 || list.Captures[0].ID != "px_complete" {
		t.Fatal(out.Code, out.Body.String())
	}
	// Unsafe files are not confused with a safe but incomplete write/crash.
	path := filepath.Join(s.payloadDebug.root, "px_partial", "manifest.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(c.dir, "manifest.json"), path); err != nil {
		t.Skip(err)
	}
	out = adminRequest(s, "GET", "debug/captures", nil, cookie, csrf)
	if out.Code != 409 || strings.Contains(out.Body.String(), path) {
		t.Fatal("unsafe path disclosed or accepted", out.Code, out.Body.String())
	}
}

func TestPayloadDebugAdminRecordLimitAndNewestWindow(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	enableDebugAdmin(t, s, cookie, csrf)
	for i := 0; i < debugRecordLimit; i++ {
		rec, req := debugRequest(fmt.Sprintf("px_%04d", i))
		rec.Started = int64(i + 1)
		c := s.beginPayloadCapture(rec, req)
		if c == nil {
			t.Fatal("capture stopped before record limit", i)
		}
		c.finish(rec)
	}
	rec, req := debugRequest("px_overflow")
	rec.Started = debugRecordLimit + 1
	c := s.beginPayloadCapture(rec, req)
	if c == nil {
		t.Fatal("oldest capture did not rotate out")
	}
	c.finish(rec)
	if out := adminRequest(s, "GET", "debug/captures/px_0000", nil, cookie, csrf); out.Code != http.StatusNotFound {
		t.Fatalf("evicted capture still available: %d %s", out.Code, out.Body.String())
	}
	for _, tt := range []struct {
		query string
		want  int
	}{{"", 50}, {"?limit=100", 100}} {
		out := adminRequest(s, "GET", "debug/captures"+tt.query, nil, cookie, csrf)
		var list struct {
			Captures []payloadCaptureSummary `json:"captures"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if out.Code != 200 || len(list.Captures) != tt.want || list.Captures[0].ID != "px_overflow" {
			t.Fatal(out.Code, len(list.Captures), out.Body.String())
		}
	}
	status, err := s.payloadDebugStatus()
	if err != nil || status.RecordCount != debugRecordLimit {
		t.Fatal(status, err)
	}
}
