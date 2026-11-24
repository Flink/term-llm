package serve

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/testutil/mediafixture"
)

func TestHTMLMediaZipDownload(t *testing.T) {
	s := newMediaDownloadTestServer(t)
	sourceDir := t.TempDir()
	const html = `<!doctype html><script src="local.js"></script><h1>Preview</h1>`
	if err := os.WriteFile(filepath.Join(sourceDir, "local.js"), []byte("not in the archive"), 0600); err != nil {
		t.Fatal(err)
	}
	mediaURL := publishDownloadTestMedia(t, s, filepath.Join(sourceDir, "demo.html"), "text/html", []byte(html))
	linkName := strings.Repeat("f", 32) + ".html"
	if err := os.Symlink(filepath.Base(mediaURL), filepath.Join(s.mediaPublisher.dir, linkName)); err != nil {
		t.Fatal(err)
	}
	mux := s.httpHandler()
	for _, tc := range []struct {
		name, method, path string
		auth               bool
		status             int
	}{
		{"unauthorized", "GET", mediaURL, false, 401},
		{"download", "GET", mediaURL, true, 200},
		{"head", "HEAD", mediaURL, true, 200},
		{"post", "POST", mediaURL, true, 405},
		{"noncanonical", "GET", "/chat/media/demo.html", true, 404},
		{"nested", "GET", "/chat/media/nested/" + filepath.Base(mediaURL), true, 404},
		{"symlink", "GET", "/chat/media/" + linkName, true, 404},
		{"missing", "GET", "/chat/media/" + strings.Repeat("0", 32) + ".html", true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := mediaDownloadRequest(mux, tc.method, tc.path+"?download=zip", tc.auth, "")
			if rec.Code != tc.status {
				t.Fatalf("response = %d, want %d", rec.Code, tc.status)
			}
			h := rec.Header()
			if tc.status != http.StatusOK {
				if h.Get("Content-Disposition") != "" {
					t.Fatalf("unexpected attachment on error: %v", h)
				}
				return
			}
			if h.Get("Content-Type") != "application/zip" || h.Get("Content-Disposition") != `attachment; filename="preview.zip"` ||
				h.Get("Cache-Control") != "private, no-store" || h.Get("Content-Security-Policy") != passiveFileCSP ||
				h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Vary") != "Authorization, Cookie" {
				t.Fatalf("headers = %v", h)
			}
			if tc.method == http.MethodHead {
				if rec.Body.Len() != 0 {
					t.Fatal("HEAD sent a body")
				}
				return
			}
			assertHTMLZip(t, rec.Body.Bytes(), html)
		})
	}

	preview := mediaDownloadRequest(mux, "GET", mediaURL+"?download=demo.html", true, "")
	if preview.Code != http.StatusOK || preview.Body.String() != html || preview.Header().Get("Content-Disposition") != `inline; filename="media.html"` ||
		preview.Header().Get("Content-Security-Policy") != interactiveHTMLCSP {
		t.Fatalf("HTML filename request changed preview: %d %v", preview.Code, preview.Header())
	}
}

func assertHTMLZip(t *testing.T, data []byte, want string) {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "index.html" {
		t.Fatalf("unexpected archive entries: %v", archive.File)
	}
	f, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || string(got) != want {
		t.Fatalf("archived HTML = %q, %v; want %q", got, err, want)
	}
}

func TestPassiveMediaNamedDownload(t *testing.T) {
	s := newMediaDownloadTestServer(t)
	sourceDir := t.TempDir()
	text := []byte("puts 'hello'\n")
	markdown := []byte("# Notes\n")
	video := mediafixture.MP4("isom", "vide")
	textURL := publishDownloadTestMedia(t, s, filepath.Join(sourceDir, "script.rb"), "text/plain", text)
	mdURL := publishDownloadTestMedia(t, s, filepath.Join(sourceDir, "notes.md"), "text/markdown", markdown)
	videoURL := publishDownloadTestMedia(t, s, filepath.Join(sourceDir, "video.mp4"), "video/mp4", video)
	const legacyURL = "/chat/media/legacy.mp4"
	if err := os.WriteFile(filepath.Join(s.mediaPublisher.dir, filepath.Base(legacyURL)), video, 0600); err != nil {
		t.Fatal(err)
	}
	mux := s.httpHandler()
	for _, tc := range []struct {
		name, path, download, disposition, filename, contentType, byteRange string
		body                                                                []byte
		status                                                              int
	}{
		{"text", textURL, "script.rb", "attachment", "script.rb.txt", "text/plain; charset=utf-8", "", text, 200},
		{"markdown", mdURL, `Résumé "notes".markdown`, "attachment", `Résumé "notes".markdown`, "text/markdown; charset=utf-8", "", markdown, 200},
		{"video range", videoURL, "original clip.mp4", "attachment", "original clip.mp4", "video/mp4", "bytes=0-3", video[:4], 206},
		{"legacy", legacyURL, "original.mp4", "inline", "media.mp4", "video/mp4", "", video, 200},
		{"slash", textURL, "../script.rb", "attachment", "media.txt", "text/plain; charset=utf-8", "", text, 200},
		{"backslash", textURL, `C:\private\script.rb`, "attachment", "media.txt", "text/plain; charset=utf-8", "", text, 200},
		{"control", textURL, "script.rb\r\nX-Test: test", "attachment", "media.txt", "text/plain; charset=utf-8", "", text, 200},
		{"oversize", textURL, strings.Repeat("é", 128), "attachment", "media.txt", "text/plain; charset=utf-8", "", text, 200},
		{"html hint", textURL, "spoof.HTML", "attachment", "spoof.HTML.txt", "text/plain; charset=utf-8", "", text, 200},
		{"svg hint", textURL, "spoof.svg", "attachment", "spoof.svg.txt", "text/plain; charset=utf-8", "", text, 200},
		{"executable hint", textURL, "spoof.exe", "attachment", "spoof.exe.txt", "text/plain; charset=utf-8", "", text, 200},
		{"mismatched video extension", videoURL, "movie.html", "attachment", "movie.html.mp4", "video/mp4", "", video, 200},
		{"known source", textURL, "main.go", "attachment", "main.go", "text/plain; charset=utf-8", "", text, 200},
		{"suffix only", textURL, ".txt", "attachment", "media.txt", "text/plain; charset=utf-8", "", text, 200},
		{"empty", textURL, "", "attachment", "media.txt", "text/plain; charset=utf-8", "", text, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := mediaDownloadRequest(mux, "GET", tc.path+"?download="+url.QueryEscape(tc.download), true, tc.byteRange)
			h := rec.Header()
			disposition, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
			if err != nil || disposition != tc.disposition || params["filename"] != tc.filename {
				t.Fatalf("disposition = %q (%v)", h.Get("Content-Disposition"), err)
			}
			if rec.Code != tc.status || !bytes.Equal(rec.Body.Bytes(), tc.body) || h.Get("Content-Type") != tc.contentType ||
				h.Get("Content-Security-Policy") != passiveFileCSP || h.Get("X-Content-Type-Options") != "nosniff" ||
				h.Get("Cache-Control") != "private, max-age=86400" || h.Get("Accept-Ranges") != "bytes" {
				t.Fatalf("response = %d %v, %d body bytes", rec.Code, h, rec.Body.Len())
			}
		})
	}
}

func newMediaDownloadTestServer(t *testing.T) *serveServer {
	t.Helper()
	return &serveServer{
		cfg:            serveServerConfig{basePath: "/chat", requireAuth: true, token: "download-test"},
		cfgRef:         &config.Config{},
		mediaPublisher: &serveMediaPublisher{dir: t.TempDir()},
	}
}

func publishDownloadTestMedia(t *testing.T, s *serveServer, path, mediaType string, body []byte) string {
	t.Helper()
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	stored, err := s.mediaPublisher.PublishMedia(context.Background(), path, mediaType)
	if err != nil {
		t.Fatal(err)
	}
	return s.mediaURL(stored)
}

func mediaDownloadRequest(handler http.Handler, method, path string, auth bool, byteRange string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if auth {
		req.Header.Set("Authorization", "Bearer download-test")
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
