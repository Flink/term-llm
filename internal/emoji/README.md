# Emoji completion data

`catalog.json` is used by the Go chat composer. Each row
contains a fully qualified glyph (possibly multiple code points), its English
CLDR short name, additional English keyword words, and (when different from
the normalized CLDR name) space-separated GitHub/gemoji shortcodes, preferred
alias first. The shortcodes are lookup labels, not Unicode names. Rows remain
in Unicode keyboard/display order; only Unicode glyphs are inserted, no images.

To update the data, update the versioned URLs and SHA-256 checksums in
`generate.py`, then run `go generate ./internal/emoji` and review the diff.
Only regeneration needs Python 3 and internet access; builds and searches use
the checked-in Unicode Emoji 18.0, CLDR 49, and gemoji 4.1.0 data. See
`LICENSE` for the Unicode data license and `LICENSE.gemoji` for the alias license.
