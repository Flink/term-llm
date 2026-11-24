package serve

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestHTMLMediaDelivery(t *testing.T) {
	dir := t.TempDir()
	p := &serveMediaPublisher{dir: dir}
	source := filepath.Join(t.TempDir(), "demo.htm")
	const body = `<!doctype html><html><head></head><body><button onclick="this.textContent='done'">Run</button></body></html>`
	if err := os.WriteFile(source, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	stored, err := p.PublishMedia(context.Background(), source, "text/html")
	if err != nil {
		t.Fatal(err)
	}
	s := &serveServer{cfg: serveServerConfig{basePath: "/chat", filesDir: dir, requireAuth: true, token: "tkn-123", ui: true}, cfgRef: &config.Config{}, mediaPublisher: p}
	s.cfgRef.Image.OutputDir = dir
	entries := s.toolMediaEntries([]core.MediaArtifact{{Reference: "ref", StoredPath: stored, MediaType: "text/html"}})
	if len(entries) != 1 || entries[0].URL != "/chat/media/"+filepath.Base(stored) {
		t.Fatalf("entries: %+v", entries)
	}
	mux := s.httpHandler()
	url := entries[0].URL
	for _, tc := range []struct {
		method, dest, rng string
		auth              bool
		status            int
	}{{"GET", "document", "", false, 401}, {"HEAD", "document", "", true, 200}, {"GET", "iframe", "", true, 200}, {"GET", "document", "", true, 200}, {"GET", "document", "bytes=0-14", true, 206}} {
		req := httptest.NewRequest(tc.method, url, nil)
		req.Header.Set("Sec-Fetch-Dest", tc.dest)
		req.Header.Set("Range", tc.rng)
		if tc.auth {
			req.Header.Set("Authorization", "Bearer tkn-123")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%+v: %d: %s", tc, rec.Code, rec.Body.String())
		}
		if !tc.auth {
			continue
		}
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("headers: %v", rec.Header())
		}
		csp := "; " + rec.Header().Get("Content-Security-Policy") + ";"
		for _, directive := range []string{"sandbox allow-scripts", "default-src 'none'", "script-src 'unsafe-inline'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'self'", "frame-src 'none'", "connect-src 'none'", "worker-src 'none'"} {
			if !strings.Contains(csp, "; "+directive+";") {
				t.Fatalf("missing or broadened %s: %s", directive, csp)
			}
		}
		if tc.method == "GET" && tc.rng == "" && rec.Body.String() != body {
			t.Fatal("published document changed")
		}
	}
	for _, prefix := range []string{"files", "images"} {
		req := httptest.NewRequest("GET", "/chat/"+prefix+"/"+filepath.Base(stored), nil)
		req.Header.Set("Authorization", "Bearer tkn-123")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != passiveFileCSP || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("alternate %s: %d %v", prefix, rec.Code, rec.Header())
		}
	}
	hub := hubWithBackend(t, "/chat", mux.ServeHTTP)
	rec := httptest.NewRecorder()
	hub.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/node/alpha"+strings.TrimPrefix(url, "/chat"), nil))
	if rec.Code != 200 || rec.Body.String() != body || rec.Header().Get("Content-Security-Policy") != interactiveHTMLCSP {
		t.Fatalf("Hub rewrote artifact: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	for _, prefix := range []string{"files", "images"} {
		rec := httptest.NewRecorder()
		hub.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/node/alpha/"+prefix+"/"+filepath.Base(stored), nil))
		if rec.Code != 200 || rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Content-Security-Policy") != passiveFileCSP || rec.Body.String() != body {
			t.Fatalf("Hub altered passive %s document: %d %v %s", prefix, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	for _, path := range []string{"/chat/", "/node/alpha/"} {
		handler := mux
		if path == "/node/alpha/" {
			handler = hub.handler()
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "frame-src 'self'" || !strings.Contains(rec.Body.String(), "TERM_LLM_UI_PREFIX") {
			t.Fatalf("chat shell %s: %d %v", path, rec.Code, rec.Header())
		}
		if path == "/node/alpha/" && !strings.Contains(rec.Body.String(), "window.TERM_LLM_HUB=") {
			t.Fatal("Hub did not inject trusted shell bootstrap")
		}
	}
}

func TestHTMLPublisherRejectsInvalidCopy(t *testing.T) {
	p := &serveMediaPublisher{dir: t.TempDir()}
	source := filepath.Join(t.TempDir(), "invalid.html")
	for _, data := range [][]byte{nil, {0xff}, {'a', 0}, make([]byte, (4<<20)+1)} {
		if err := os.WriteFile(source, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.PublishMedia(context.Background(), source, "text/html"); err == nil {
			t.Fatal("invalid copy accepted")
		}
	}
	files, err := os.ReadDir(p.dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("files after rejection: %v, %v", files, err)
	}
}

func TestHTMLMediaStoreIsolation(t *testing.T) {
	dir := t.TempDir()
	s := &serveServer{mediaPublisher: &serveMediaPublisher{dir: dir}}
	hash := strings.Repeat("a", 32)
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("<!doctype html><title>Demo</title>"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stored := write(hash + ".html")
	for _, name := range []string{"demo.html", hash + ".htm", strings.ToUpper(hash) + ".html", "nested/" + hash + ".html"} {
		write(name)
		rec := httptest.NewRecorder()
		s.handleMedia(rec, httptest.NewRequest("GET", "/media/"+name, nil))
		if rec.Code != 404 || s.mediaURL(filepath.Join(dir, name)) != "" {
			t.Errorf("noncanonical %s: %d", name, rec.Code)
		}
	}
	link := strings.Repeat("b", 32) + ".html"
	if err := os.Symlink(stored, filepath.Join(dir, link)); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleMedia(rec, httptest.NewRequest("GET", "/media/"+link, nil))
	if rec.Code != 404 || s.mediaURL(filepath.Join(dir, link)) != "" {
		t.Errorf("store symlink accepted: %d", rec.Code)
	}
	for _, artifact := range []core.MediaArtifact{{StoredPath: stored, MediaType: "image/png"}, {StoredPath: write("fixture.png"), MediaType: "text/html"}} {
		if entries := s.toolMediaEntries([]core.MediaArtifact{artifact}); len(entries) != 0 {
			t.Errorf("projected mismatched artifact: %+v", entries)
		}
	}
}
