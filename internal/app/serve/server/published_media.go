package server

import (
	"archive/zip"
	"bytes"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/term-llm/term-llm/internal/app/serve/servepath"
	"github.com/term-llm/term-llm/internal/mediatype"
)

const PassiveFileCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'"

const InteractiveHTMLCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; media-src data:; font-src data:; connect-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// ServePublishedMedia serves an authorized GET or HEAD from the publication store.
func ServePublishedMedia(w http.ResponseWriter, r *http.Request, dir string) {
	name := strings.TrimPrefix(r.URL.Path, "/media/")
	mediaType, ok := servepath.PublishedMediaType(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, opened, err := servepath.OpenMedia(dir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", PassiveFileCSP)
	h.Set("Cache-Control", "private, max-age=86400")
	h.Add("Vary", "Authorization, Cookie")
	downloads := r.URL.Query()["download"]
	var download string
	if len(downloads) == 1 {
		download = downloads[0]
	}
	if mediaType == "text/html" {
		h.Set("Cache-Control", "private, no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-DNS-Prefetch-Control", "off")
		if download == "zip" {
			serveHTMLZip(w, r, f, opened.Size())
			return
		}
		h.Set("Content-Security-Policy", InteractiveHTMLCSP)
	}
	if strings.HasPrefix(mediaType, "text/") {
		h.Set("Content-Type", mediaType+"; charset=utf-8")
	} else {
		h.Set("Content-Type", mediaType)
	}
	ext := filepath.Ext(name)
	disposition := `inline; filename="media` + ext + `"`
	if mediaType != "text/html" && len(downloads) != 0 {
		if attachment := passiveMediaAttachment(name, ext, mediaType, download); attachment != "" {
			disposition = attachment
		}
	}
	h.Set("Content-Disposition", disposition)
	http.ServeContent(w, r, name, opened.ModTime(), f)
}

func serveHTMLZip(w http.ResponseWriter, r *http.Request, source io.Reader, size int64) {
	if size <= 0 || size > mediatype.HTMLFormat().MaxBytes {
		http.NotFound(w, r)
		return
	}
	var body bytes.Buffer
	if r.Method == http.MethodGet {
		// Finish the bounded archive before committing the attachment headers.
		// Only copy the published HTML; never resolve references in the document.
		archive := zip.NewWriter(&body)
		entry, err := archive.Create("index.html")
		if err == nil {
			_, err = io.CopyN(entry, source, size)
		}
		closeErr := archive.Close()
		if err != nil || closeErr != nil {
			http.Error(w, "could not create ZIP download", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="preview.zip"`)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body.Bytes())
	}
}

func passiveMediaAttachment(storedName, ext, mediaType, download string) string {
	// Keep legacy binary URLs working without changing their download behavior.
	if !servepath.IsCanonicalMediaName(storedName) {
		return ""
	}

	name := safePassiveDownloadName(download, mediaType, ext)
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}

func safePassiveDownloadName(download, mediaType, ext string) string {
	// Reject paths on every OS and bound the name before encoding the header.
	const maxFilenameBytes = 255
	fallback := "media" + ext
	name := strings.TrimSpace(download)
	if name == "" || len(name) > maxFilenameBytes || !utf8.ValidString(name) ||
		strings.ContainsAny(name, `/\:`) || strings.ContainsFunc(download, unicode.IsControl) {
		return fallback
	}

	// Do not let arbitrary content acquire a browser-active or native
	// executable extension. Preserve only the media suffix and known text names.
	originalSuffix := filepath.Ext(name)
	suffix := strings.ToLower(originalSuffix)
	if name == originalSuffix {
		return fallback
	}
	if suffix == ext {
		return name
	}
	if strings.HasPrefix(mediaType, "text/") {
		switch suffix {
		case ".txt", ".md", ".markdown", ".go", ".ts", ".tsx", ".json", ".yaml", ".yml", ".toml", ".css", ".sql", ".rs", ".c", ".h", ".cpp", ".csv", ".log", ".diff", ".patch":
			return name
		}
	}
	if len(name)+len(ext) > maxFilenameBytes {
		return fallback
	}
	return name + ext
}
