# Terminal width data

`EastAsianWidth-18.0.0.txt` is the unmodified Unicode 18.0.0 character-width data:
https://www.unicode.org/Public/18.0.0/ucd/EastAsianWidth.txt

SHA-256: `a0cf29eacd00cfcaec4381c6b7c281685f18dbb4e7ff82b4076ccb342ca839aa`

`LICENSE.txt` contains the Unicode data license. Regenerate the editor's compact
wide/fullwidth range table from the repository root with:

```sh
go generate ./cmd/lmc
```

Generation verifies the checksum and requires no network access. To update the
data, replace the versioned source, update the generator's source URL and
checksum, regenerate, and run the editor width tests and benchmarks.

The table supplies wide/fullwidth classification for the rune-based editor.
Control escaping, tab expansion, and combining-mark widths are handled before
this lookup. Ambiguous characters use one cell. Font-dependent shaping and
grapheme-aware cursor movement are not specified by this table.
