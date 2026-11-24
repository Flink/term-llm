package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestServeTextMediaDelivery(t *testing.T) {
	for _, tc := range []struct{ name, mime, ext string }{
		{"notes.MARKDOWN", "text/markdown", ".md"},
		{"source.rb", "text/plain", ".txt"},
	} {
		t.Run(tc.mime, func(t *testing.T) {
			const body = "# résumé\n<script>never HTML</script>\n"
			source := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(source, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			p := &serveMediaPublisher{dir: t.TempDir()}
			stored, err := p.PublishMedia(context.Background(), source, tc.mime)
			if err != nil || filepath.Ext(stored) != tc.ext {
				t.Fatalf("publish = %q, %v", stored, err)
			}
			s := &serveServer{mediaPublisher: p}
			item := core.MediaArtifact{Reference: "ref", StoredPath: stored, MediaType: tc.mime, Name: tc.name, Caption: "Report"}
			entries := s.toolMediaEntries([]core.MediaArtifact{item})
			want := []webMediaEntry{{Reference: "ref", URL: "/media/" + filepath.Base(stored), MediaType: tc.mime, Name: tc.name, Caption: "Report"}}
			if !reflect.DeepEqual(entries, want) {
				t.Fatalf("media entries: %+v", entries)
			}
			encoded, err := json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
			if recovered := mediaEntriesFromValue(value); !reflect.DeepEqual(recovered, entries) {
				t.Fatalf("recovered entries: %+v", recovered)
			}
			rec := httptest.NewRecorder()
			s.handleMedia(rec, httptest.NewRequest("GET", entries[0].URL, nil))
			h := rec.Header()
			if rec.Code != 200 || rec.Body.String() != body || h.Get("Content-Type") != tc.mime+"; charset=utf-8" ||
				h.Get("Content-Disposition") != `inline; filename="media`+tc.ext+`"` ||
				h.Get("Content-Security-Policy") != passiveFileCSP || h.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("response: %d %v %q", rec.Code, h, rec.Body.String())
			}
		})
	}
}

func TestServeTextMediaRejectsInvalidCopy(t *testing.T) {
	for _, tc := range []struct{ mime, body string }{
		{"text/markdown", "# Markdown\x00"},
		{"text/plain", "abc\x1b"},
		{"text/plain", "OggSnot a real container"},
	} {
		p := &serveMediaPublisher{dir: t.TempDir()}
		source := filepath.Join(t.TempDir(), "source.any")
		if err := os.WriteFile(source, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		if stored, err := p.PublishMedia(context.Background(), source, tc.mime); err == nil || stored != "" {
			t.Errorf("published invalid %s: %q, %v", tc.mime, stored, err)
		}
	}
}
