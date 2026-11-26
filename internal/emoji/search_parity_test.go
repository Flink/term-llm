package emoji

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var updateParity = flag.Bool("update-emoji-parity", false, "rewrite testdata/search_parity.json from Search")

// parityQueries cover every rank, alias handling and case folding.
var parityQueries = []string{
	"star", "STAR", "rocke", "sweat", "sweat_sm", "grinning_face_with_sweat", "thumbsup", "+1", "-1",
	"heart", "polar_be", "terrestrial", "smile", "woman_as", "wave", "flag_n", "skin", "100", "ok", "cat",
}

type parityCase struct {
	Query   string      `json:"query"`
	Results [][2]string `json:"results"`
}

// frontend/src/domain/emoji-search.ts reimplements Search for the web composer;
// its test reads the same fixture, so a ranking change must update both.
func TestSearchParityFixture(t *testing.T) {
	path := filepath.Join("testdata", "search_parity.json")
	got := make([]parityCase, 0, len(parityQueries))
	for _, query := range parityQueries {
		c := parityCase{Query: query, Results: [][2]string{}}
		for _, entry := range Search(query, 8) {
			c.Results = append(c.Results, [2]string{entry.Glyph, entry.Shortcode})
		}
		got = append(got, c)
	}
	if *updateParity {
		if err := os.WriteFile(path, encodeParity(t, got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []parityCase
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(got, want, func(a, b parityCase) bool {
		return a.Query == b.Query && slices.Equal(a.Results, b.Results)
	}) {
		t.Fatalf("Search results differ from %s; rerun with -update-emoji-parity, then the web emoji tests", path)
	}
}

// encodeParity writes one query per line so ranking changes review clearly.
func encodeParity(t *testing.T, cases []parityCase) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("[\n")
	for i, c := range cases {
		var line bytes.Buffer
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(c); err != nil {
			t.Fatal(err)
		}
		out.Write(bytes.TrimSuffix(line.Bytes(), []byte("\n")))
		if i < len(cases)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString("]\n")
	return out.Bytes()
}
