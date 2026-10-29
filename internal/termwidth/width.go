// Package termwidth measures text in terminal cells.
package termwidth

import "unicode"

//go:generate go run ../../scripts/gen_terminal_width.go -input ../../testdata/unicode/EastAsianWidth-18.0.0.txt -output table.go

// RuneWidth is how many terminal cells r takes when shown: two for an East
// Asian wide or fullwidth character, none for a combining mark, one
// otherwise. Measuring a control character is the caller's job, since it is
// shown escaped.
func RuneWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if unicode.Is(wideRunes, r) {
		return 2
	}
	return 1
}
