package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// editBuffer owns one input line or pasted multiline draft. Cursor positions
// are rune offsets; the limit counts UTF-8 bytes, as the request reader does.
type editBuffer struct {
	editState
	text           []rune
	limit          int
	history        []replPrompt
	historyAt      int
	saved          editSnapshot
	searching      bool
	query          []rune
	queryBytes     int
	queryRejected  bool
	searchAt       int
	searchOriginal editSnapshot
}

// editState travels with the draft through history and search. Save it as one
// value so restoring text cannot omit a cursor, size, or interpretation flag.
type editState struct {
	cursor, bytes, line, lines int
	literal, tooLong           bool
	escapedSlash               bool
	// Once rejected, keep bounded tails on both sides of the insertion point.
	// lastBytes replaces tail without mutating it, so snapshots can share it.
	tail       []byte
	tailSuffix string
}

type editSnapshot struct {
	editState
	text string
}

func (e *editBuffer) snapshot() editSnapshot {
	return editSnapshot{editState: e.editState, text: string(e.text)}
}

func (e *editBuffer) restore(s editSnapshot) {
	e.text = []rune(s.text)
	e.editState = s.editState
}

func newEditBuffer(limit int, history *replHistory) *editBuffer {
	e := &editBuffer{limit: limit, editState: editState{line: 1, lines: 1}}
	if history != nil {
		e.history = history.entries
	}
	e.historyAt = len(e.history)
	return e
}

func (e *editBuffer) replace(text string) {
	text = validEditText(text)
	e.text = []rune(text)
	e.cursor, e.bytes = len(e.text), len(text)
	e.tooLong = e.bytes > e.limit
	e.tail = nil
	e.tailSuffix, e.escapedSlash = "", false
	if e.tooLong {
		e.tail = lastBytes(nil, []byte(text[max(0, len(text)-lineTailBytes):]))
	}
	e.lines = 1 + strings.Count(text, "\n")
	e.line = e.lines
}

func validEditText(text string) string {
	if !utf8.ValidString(text) {
		return string([]rune(text))
	}
	return text
}

func (e *editBuffer) insert(text string) {
	text = validEditText(text)
	if e.tooLong {
		e.tail = lastBytes(e.tail, []byte(text))
		return
	}
	if e.bytes+len(text) > e.limit {
		e.tooLong = true
		// Only a bounded suffix is needed, even for a very large draft.
		e.tail = lastBytes(nil, []byte(string(e.text[max(0, e.cursor-lineTailBytes):e.cursor])))
		e.tail = lastBytes(e.tail, []byte(text))
		suffix := string(e.text[max(e.cursor, len(e.text)-lineTailBytes):])
		e.tailSuffix = string(lastBytes(nil, []byte(suffix)))
		return
	}
	if e.cursor == 0 && text != "" {
		e.escapedSlash = false
	}
	added := []rune(text)
	e.text = append(e.text, make([]rune, len(added))...)
	copy(e.text[e.cursor+len(added):], e.text[e.cursor:len(e.text)-len(added)])
	copy(e.text[e.cursor:], added)
	e.cursor += len(added)
	e.bytes += len(text)
	lines := strings.Count(text, "\n")
	e.line += lines
	e.lines += lines
}

func (e *editBuffer) remove(start, end int) {
	if start == 0 && end > start {
		e.escapedSlash = false
	}
	e.moveCursor(start)
	for _, r := range e.text[start:end] {
		e.bytes -= utf8.RuneLen(r)
		if r == '\n' {
			e.lines--
		}
	}
	e.text = append(e.text[:start], e.text[end:]...)
	e.cursor = start
	if len(e.text) == 0 && !e.tooLong {
		e.literal = false
	}
}

// inputLine applies the same rejection contract as the canonical reader: no
// partial text, but enough tail to discard the complete continued submission.
func (e *editBuffer) inputLine(eof bool) inputLine {
	line := inputLine{eof: eof, literal: e.literal, tooLong: e.tooLong, escapedSlash: e.escapedSlash}
	if e.tooLong {
		line.tail = string(lastBytes(e.tail, []byte(e.tailSuffix)))
	} else {
		line.text = string(e.text)
	}
	return line
}

func (e *editBuffer) moveCursor(cursor int) {
	cursor = max(0, min(len(e.text), cursor))
	for _, r := range e.text[min(cursor, e.cursor):max(cursor, e.cursor)] {
		if r == '\n' {
			if cursor < e.cursor {
				e.line--
			} else {
				e.line++
			}
		}
	}
	e.cursor = cursor
}

func (e *editBuffer) recall(direction int) {
	if len(e.history) == 0 {
		return
	}
	if e.historyAt == len(e.history) {
		e.saved = e.snapshot()
	}
	e.historyAt = max(0, min(len(e.history), e.historyAt+direction))
	if e.historyAt == len(e.history) {
		e.restore(e.saved)
	} else {
		e.replaceHistory(e.history[e.historyAt].text)
	}
}

func (e *editBuffer) replaceHistory(text string) {
	// History holds submitted prompt text after // was unescaped. Restore
	// the escape when recalling it, so a literal /exit cannot become a command.
	escaped := strings.HasPrefix(text, "/")
	if escaped {
		text = "/" + text
	}
	e.replace(text)
	e.literal = true
	e.escapedSlash = escaped
}

func (e *editBuffer) search(older bool) {
	if !e.searching {
		e.searching = true
		e.searchOriginal = e.snapshot()
		e.query = nil
		e.queryBytes, e.queryRejected = 0, false
		e.searchAt = len(e.history)
	}
	start := len(e.history) - 1
	if older {
		start = e.searchAt - 1
	}
	query := string(e.query)
	for i := start; i >= 0; i-- {
		if strings.Contains(e.history[i].text, query) {
			e.replaceHistory(e.history[i].text)
			e.searchAt = i
			return
		}
	}
	e.searchAt = -1
	e.restore(e.searchOriginal)
}

func (e *editBuffer) addQuery(text string, tooLong bool) {
	text = validEditText(text)
	if tooLong || e.queryBytes+len(text) > e.limit {
		e.queryRejected = true
		return
	}
	e.queryRejected = false
	e.query = append(e.query, []rune(text)...)
	e.queryBytes += len(text)
	e.search(false)
}

func (e *editBuffer) pasteLimit() int {
	if e.searching {
		return max(0, e.limit-e.queryBytes)
	}
	return max(0, e.limit-e.bytes)
}

// Paste follows the active input mode. A search query never edits its candidate.
func (e *editBuffer) paste(text string, tooLong bool) {
	if e.searching {
		e.addQuery(text, tooLong)
		return
	}
	if tooLong {
		e.tooLong = true
	} else {
		e.insert(text)
	}
	e.literal = true
}

func (e *editBuffer) key(key string) {
	if e.searching {
		switch key {
		case "\r", "\n":
			e.searching = false
			return
		case "\x1b":
			e.searching = false
			e.restore(e.searchOriginal)
			return
		case "\x12":
			e.search(true)
			return
		case "\x7f", "\b":
			if len(e.query) > 0 {
				e.queryBytes -= utf8.RuneLen(e.query[len(e.query)-1])
				e.query = e.query[:len(e.query)-1]
			}
			e.queryRejected = false
		default:
			if !strings.HasPrefix(key, "\x1b") && len(key) > 0 && !unicode.IsControl([]rune(key)[0]) {
				e.addQuery(key, false)
			}
			return
		}
		e.search(false)
		return
	}
	switch key {
	case "\x1b[A":
		e.recall(-1)
	case "\x1b[B":
		e.recall(1)
	case "\x1b[D", "\x02":
		e.moveCursor(e.cursor - 1)
	case "\x1b[C", "\x06":
		e.moveCursor(e.cursor + 1)
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~", "\x01":
		for e.cursor > 0 && e.text[e.cursor-1] != '\n' {
			e.cursor--
		}
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~", "\x05":
		for e.cursor < len(e.text) && e.text[e.cursor] != '\n' {
			e.cursor++
		}
	case "\x7f", "\b":
		if e.cursor > 0 {
			e.remove(e.cursor-1, e.cursor)
		}
	case "\x1b[3~":
		if e.cursor < len(e.text) {
			e.remove(e.cursor, e.cursor+1)
		}
	case "\x17":
		start := e.cursor
		for start > 0 && unicode.IsSpace(e.text[start-1]) {
			start--
		}
		for start > 0 && !unicode.IsSpace(e.text[start-1]) {
			start--
		}
		e.remove(start, e.cursor)
	case "\x15":
		e.replace("")
		e.literal = false
	case "\x0b":
		e.remove(e.cursor, len(e.text))
	case "\x12":
		e.search(false)
	default:
		if key == "\t" || (len(key) > 0 && !strings.HasPrefix(key, "\x1b") && !unicode.IsControl([]rune(key)[0])) {
			e.insert(key)
		}
	}
}

// view visits only the visible span around the cursor. Line counts are updated
// by edits, so repainting a long draft never scans or copies the whole draft.
func (e *editBuffer) view(prompt string, columns int) (text string, cursor int) {
	if columns < 5 {
		return strings.Repeat(">", min(1, max(0, columns-1))), 0
	}
	if e.searching && e.queryRejected {
		message := "> [search query too long; previous query kept]"
		return message[:min(len(message), columns-1)], 2
	}
	if e.tooLong && !e.searching {
		message := "> [input too long; Ctrl-U clears]"
		return message[:min(len(message), columns-1)], 2
	}
	if e.searching {
		state := "search"
		if e.searchAt < 0 {
			state = "no match"
		}
		// Quote only a bounded preview, even when the query is megabytes long.
		n := min(len(e.query), max(1, columns/12))
		query := strconv.QuoteToASCII(string(e.query[:n]))
		if n < len(e.query) {
			query += "..."
		}
		prompt = state + " " + query + ": "
	} else if e.lines > 1 {
		prompt = fmt.Sprintf("[%d/%d] %s", e.line, e.lines, prompt)
	}
	if len(prompt) > columns/2 {
		prompt = "> "
	}
	available := max(1, columns-len(prompt)-2)
	// A run of combining characters consumes no cells. Bound rune work too,
	// clipping only the view, never the text that will be submitted.
	maxRunes := columns * 8
	left, width := e.cursor, 0
	for left > 0 && e.text[left-1] != '\n' && e.cursor-left < maxRunes {
		n := editRuneWidth(e.text[left-1])
		if width+n > available-1 {
			break
		}
		width += n
		left--
	}
	var b strings.Builder
	b.Grow(columns)
	b.WriteString(prompt)
	used := 0
	if left > 0 && e.text[left-1] != '\n' {
		b.WriteByte('<')
		used++
	}
	cursor = len(prompt) + used + width
	i := left
	for i < len(e.text) && e.text[i] != '\n' && i-left < maxRunes {
		r := e.text[i]
		n := editRuneWidth(r)
		if used+n > available {
			break
		}
		writeEditRune(&b, r)
		used += n
		i++
	}
	if i < len(e.text) && e.text[i] != '\n' {
		b.WriteByte('>')
	}
	return b.String(), cursor
}

//go:generate go run ../../scripts/gen_terminal_width.go -input ../../testdata/unicode/EastAsianWidth-18.0.0.txt -output repl_width_table.go

func editRuneWidth(r rune) int {
	if r == '\t' {
		return 4
	}
	if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
		if r < 0x10000 {
			return 6
		}
		if r < 0x100000 {
			return 7
		}
		return 8
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if unicode.Is(editWideRunes, r) {
		return 2
	}
	return 1
}

func writeEditRune(b *strings.Builder, r rune) {
	switch {
	case r == '\t':
		b.WriteString("    ")
	case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		fmt.Fprintf(b, "\\u%04x", r)
	default:
		b.WriteRune(r)
	}
}
