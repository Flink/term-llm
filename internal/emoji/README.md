# Emoji completion data

`catalog.json` is shared by the Go chat composer and the web composer. Each row
contains a fully qualified glyph (possibly multiple code points), its English
CLDR short name, additional English keyword words, and (when different from
the normalized CLDR name) space-separated GitHub/gemoji shortcodes, preferred
alias first. The shortcodes are lookup labels, not Unicode names. Rows remain
in Unicode keyboard/display order; only Unicode glyphs are inserted, no images.

The web composer ranks matches in `frontend/src/domain/emoji-search.ts`, a
port of `Search`. Both test suites check `testdata/search_parity.json`; after
changing either ranking or the data, run
`scripts/gotest.sh ./internal/emoji -run TestSearchParityFixture -update-emoji-parity`,
review the diff, and run the frontend tests.

To update the data, update the versioned URLs and SHA-256 checksums in
`generate.py`, then run `go generate ./internal/emoji` and review the diff.
Only regeneration needs Python 3 and internet access; builds and searches use
the checked-in Unicode Emoji 18.0, CLDR 49, and gemoji 4.1.0 data. See
`LICENSE` for the Unicode data license and `LICENSE.gemoji` for the alias license.
