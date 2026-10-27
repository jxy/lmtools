package main

import (
	"io"
	"strings"
	"unicode"
)

// thinkingQuote marks each line of visible thinking on a terminal, so a long
// thinking section scrolled halfway cannot be read as the answer below it.
const thinkingQuote = "| "

// thinkingText writes one visible thinking section to the diagnostic stream.
//
// It ends every line it starts as soon as the provider ends it, so a status
// line can follow a finished paragraph, and holds blank lines back until more
// text arrives: a summary's trailing blank lines would otherwise sit between
// its last sentence and the end marker. On a terminal each line is quoted
// with thinkingQuote, words wrap at the terminal width, and control
// characters are shown escaped; elsewhere the text passes through unchanged
// apart from those line endings.
type thinkingText struct {
	out    io.Writer
	quote  string
	width  func() int
	escape bool

	atStart bool
	blank   int
	cells   int
	word    []rune
	// spaces are held with the word that follows them, so a wrapped line
	// does not end in the space it broke at.
	spaces int
	// continuing records that the word being collected continues one
	// already placed, which moved to its own line when it reached the end of
	// one; the rest follows it without a break.
	continuing bool
	wrote      bool
	buf        strings.Builder
}

func (t *thinkingText) reset() {
	t.atStart, t.blank, t.cells, t.word, t.spaces, t.continuing, t.wrote = true, 0, 0, t.word[:0], 0, false, false
}

// write places text, holding the last word back when wrapping until the space
// or line end that completes it, or until it reaches the end of the line. A
// wide character is placed at once: lines may break at any of them, as
// between the characters of Chinese and Japanese, which use no spaces.
func (t *thinkingText) write(text string) {
	for _, r := range text {
		switch {
		case r == '\r':
		case r == '\n':
			t.placeWord()
			t.spaces, t.continuing = 0, false
			if t.atStart {
				if t.wrote {
					t.blank++
				}
				continue
			}
			t.buf.WriteByte('\n')
			t.atStart, t.cells = true, 0
		case t.width != nil && (r == ' ' || r == '\t'):
			t.startLine()
			t.placeWord()
			t.continuing = false
			t.spaces++
		default:
			t.startLine()
			if t.width == nil {
				t.writeRune(r)
				continue
			}
			if unicode.Is(editWideRunes, r) {
				t.placeWord()
				t.continuing = false
				t.word = append(t.word, r)
				t.placeWord()
				continue
			}
			t.word = append(t.word, r)
			if t.cells+t.spaces+t.wordCells() > t.limit() {
				t.placeWord()
				t.continuing = true
			}
		}
	}
	t.flush()
}

// flushWord shows the word held for wrapping while the section stays open.
func (t *thinkingText) flushWord() {
	if len(t.word) > 0 {
		t.placeWord()
		t.continuing = true
	}
	t.flush()
}

// finish ends the section's last line and drops the blank lines held back.
func (t *thinkingText) finish() {
	t.placeWord()
	t.spaces, t.continuing = 0, false
	if !t.atStart {
		t.buf.WriteByte('\n')
	}
	t.flush()
	t.atStart, t.blank, t.cells = true, 0, 0
}

func (t *thinkingText) startLine() {
	if !t.atStart {
		return
	}
	for ; t.blank > 0; t.blank-- {
		t.buf.WriteString(strings.TrimRight(t.quote, " "))
		t.buf.WriteByte('\n')
	}
	t.buf.WriteString(t.quote)
	t.atStart, t.cells, t.wrote = false, len(t.quote), true
}

func (t *thinkingText) wordCells() int {
	cells := 0
	for _, r := range t.word {
		cells += editRuneWidth(r)
	}
	return cells
}

func (t *thinkingText) placeWord() {
	if len(t.word) == 0 {
		return
	}
	cells := t.spaces + t.wordCells()
	if !t.continuing && t.cells+cells > t.limit() && t.cells > len(t.quote) {
		t.breakLine()
	} else {
		t.buf.WriteString(strings.Repeat(" ", t.spaces))
		t.cells += t.spaces
	}
	t.spaces = 0
	for _, r := range t.word {
		if n := editRuneWidth(r); t.cells+n > t.limit() && t.cells > len(t.quote) {
			t.breakLine()
		}
		t.writeRune(r)
	}
	t.word = t.word[:0]
}

func (t *thinkingText) breakLine() {
	t.buf.WriteByte('\n')
	t.buf.WriteString(t.quote)
	t.cells = len(t.quote)
}

func (t *thinkingText) writeRune(r rune) {
	if t.escape && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
		writeEditRune(&t.buf, r)
	} else {
		t.buf.WriteRune(r)
	}
	t.cells += editRuneWidth(r)
}

// limit leaves the terminal's last column empty, so a full row never leaves
// the cursor waiting to wrap.
func (t *thinkingText) limit() int {
	if t.width == nil {
		return int(^uint(0) >> 1)
	}
	return max(len(t.quote)+8, t.width()-1)
}

func (t *thinkingText) flush() {
	if t.buf.Len() == 0 {
		return
	}
	_, _ = io.WriteString(t.out, t.buf.String())
	t.buf.Reset()
}
