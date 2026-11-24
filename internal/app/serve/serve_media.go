package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/term-llm/term-llm/internal/app/serve/servepath"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mediatype"
	"github.com/term-llm/term-llm/internal/session"
)

type webMediaEntry struct {
	Reference string `json:"reference,omitempty"`
	URL       string `json:"url"`
	MediaType string `json:"type"`
	Name      string `json:"name,omitempty"`
	Caption   string `json:"caption,omitempty"`
}

func (s *serveServer) toolMediaEntries(media []core.MediaArtifact) []webMediaEntry {
	entries := make([]webMediaEntry, 0, len(media))
	for _, item := range media {
		mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
		if storedType, ok := serveMediaTypeForName(item.StoredPath); !ok || storedType != mediaType {
			continue
		}
		mediaURL := s.mediaURL(item.StoredPath)
		if mediaURL == "" {
			continue
		}
		entries = append(entries, webMediaEntry{Reference: item.Reference, URL: mediaURL, MediaType: mediaType, Name: item.Name, Caption: item.Caption})
	}
	return entries
}

func mediaEntriesFromValue(value any) []webMediaEntry {
	if typed, ok := value.([]webMediaEntry); ok {
		return append([]webMediaEntry(nil), typed...)
	}
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	entries := make([]webMediaEntry, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		urlValue := strings.TrimSpace(stringValue(entry["url"]))
		mediaType := strings.ToLower(strings.TrimSpace(stringValue(entry["type"])))
		if _, _, supported := serveMediaExtension(mediaType); urlValue == "" || !supported {
			continue
		}
		entries = append(entries, webMediaEntry{
			Reference: strings.TrimSpace(stringValue(entry["reference"])),
			URL:       urlValue, MediaType: mediaType,
			Name: strings.TrimSpace(stringValue(entry["name"])), Caption: strings.TrimSpace(stringValue(entry["caption"])),
		})
	}
	return entries
}

type serveMediaPublisher struct {
	dir string
}

func newServeMediaPublisher() (*serveMediaPublisher, error) {
	dataDir, err := session.GetDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	return &serveMediaPublisher{dir: filepath.Join(dataDir, "media")}, nil
}

func (p *serveMediaPublisher) PublishMedia(ctx context.Context, sourcePath, mediaType string) (string, error) {
	if p == nil || strings.TrimSpace(p.dir) == "" {
		return "", fmt.Errorf("media publisher is unavailable")
	}
	ext, limit, ok := serveMediaExtension(mediaType)
	if !ok {
		return "", fmt.Errorf("unsupported media type %q", mediaType)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("source is not a regular file")
	}
	if info.Size() > limit {
		return "", fmt.Errorf("media exceeds %d MiB limit", limit>>20)
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return "", fmt.Errorf("create media directory: %w", err)
	}

	tmp, err := os.CreateTemp(p.dir, ".publish-*")
	if err != nil {
		return "", fmt.Errorf("create temporary media: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure temporary media: %w", err)
	}
	hash := sha256.New()
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: source}, N: limit + 1}
	written, err := io.Copy(io.MultiWriter(tmp, hash), limited)
	if err != nil {
		return "", fmt.Errorf("copy media: %w", err)
	}
	if written > limit {
		return "", fmt.Errorf("media exceeds %d MiB limit", limit>>20)
	}
	if info.Size() != written {
		return "", fmt.Errorf("source changed while publishing")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	inspect := mediatype.Inspect
	if ext == mediatype.HTMLFormat().Extension {
		inspect = mediatype.InspectHTML
	}
	format, inspectErr := inspect(tmp, written)
	if errors.Is(inspectErr, mediatype.ErrUnsupported) {
		return "", fmt.Errorf("published copy failed media verification: %w", inspectErr)
	}
	if inspectErr != nil {
		return "", fmt.Errorf("verify published media: %w", inspectErr)
	}
	if format.MIME != strings.ToLower(strings.TrimSpace(mediaType)) {
		return "", fmt.Errorf("media type changed while publishing")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync media: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close media: %w", err)
	}
	name := hex.EncodeToString(hash.Sum(nil)[:16]) + ext
	destination := filepath.Join(p.dir, name)
	if existing, statErr := os.Lstat(destination); statErr == nil && existing.Mode().IsRegular() {
		return destination, nil
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		if existing, statErr := os.Lstat(destination); statErr == nil && existing.Mode().IsRegular() {
			return destination, nil
		}
		return "", fmt.Errorf("publish media: %w", err)
	}
	keep = true
	return destination, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

func serveMediaExtension(mediaType string) (extension string, limit int64, ok bool) {
	if f := mediatype.HTMLFormat(); strings.EqualFold(strings.TrimSpace(mediaType), f.MIME) {
		return f.Extension, f.MaxBytes, true
	}
	f, ok := mediatype.Lookup(mediaType)
	return f.Extension, f.MaxBytes, ok
}

func serveMediaTypeForName(name string) (string, bool) { return servepath.PublishedMediaType(name) }

const passiveFileCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'"

const interactiveHTMLCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; media-src data:; font-src data:; connect-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

func (s *serveServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	if s.mediaPublisher == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/media/")
	mediaType, ok := serveMediaTypeForName(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, opened, err := servepath.OpenMedia(s.mediaPublisher.dir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", mediaType)
	h.Set("Content-Disposition", `inline; filename="media`+filepath.Ext(name)+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", passiveFileCSP)
	h.Set("Cache-Control", "private, max-age=86400")
	h.Add("Vary", "Authorization, Cookie")
	if mediaType == "text/html" {
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", interactiveHTMLCSP)
		h.Set("Cache-Control", "private, no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-DNS-Prefetch-Control", "off")
	}
	http.ServeContent(w, r, name, opened.ModTime(), f)
}

func (s *serveServer) mediaURL(storedPath string) string {
	if s == nil || s.mediaPublisher == nil || strings.TrimSpace(storedPath) == "" {
		return ""
	}
	name := servepath.StoredMediaName(s.mediaPublisher.dir, storedPath)
	if name == "" {
		return ""
	}
	return s.cfg.mediaRoute() + name
}
