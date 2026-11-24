package servepath

import "testing"

func TestPublishedDocumentTypes(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct{ name, mime string }{
		{hash + ".md", "text/markdown"}, {hash + ".txt", "text/plain"}, {hash + ".html", "text/html"},
		{hash + ".markdown", ""}, {hash + ".MD", ""}, {"notes.md", ""}, {"source.txt", ""},
		{"legacy.mp4", "video/mp4"},
	} {
		if mime, ok := PublishedMediaType(tc.name); ok != (tc.mime != "") || mime != tc.mime {
			t.Errorf("PublishedMediaType(%q) = %q, %t", tc.name, mime, ok)
		}
	}
}
