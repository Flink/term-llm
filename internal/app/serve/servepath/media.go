package servepath

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/term-llm/term-llm/internal/mediatype"
)

var publishedHTMLName = regexp.MustCompile(`^[0-9a-f]{32}\.html$`)

// PublishedMediaType maps passive media suffixes and canonical published HTML names.
func PublishedMediaType(name string) (string, bool) {
	if publishedHTMLName.MatchString(filepath.Base(name)) {
		return mediatype.HTMLFormat().MIME, true
	}
	f, ok := mediatype.ForExtension(filepath.Ext(name))
	return f.MIME, ok
}

// OpenMedia opens a regular non-symlink direct child of the publication store.
// Root confines the open even if an entry changes between Lstat and Open.
func OpenMedia(dir, name string) (*os.File, os.FileInfo, error) {
	if !filepath.IsLocal(name) || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return nil, nil, os.ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, os.ErrInvalid
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, nil, os.ErrInvalid
	}
	return f, opened, nil
}

// StoredMediaName returns the direct, regular entry suitable for a published URL, or empty.
func StoredMediaName(dir, storedPath string) string {
	canonicalDir, err := CanonicalizeExistingPath(dir)
	if err != nil {
		return ""
	}
	parent, err := CanonicalizeExistingPath(filepath.Dir(storedPath))
	if err != nil || parent != canonicalDir {
		return ""
	}
	name := filepath.Base(storedPath)
	if _, ok := PublishedMediaType(name); !ok {
		return ""
	}
	// Project only regular store entries, never the resolved target of a link.
	info, err := os.Lstat(filepath.Join(canonicalDir, name))
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return name
}
