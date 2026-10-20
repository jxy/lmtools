package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestEditBufferCursorAndHistory(t *testing.T) {
	h := &replHistory{}
	h.add("first\nsecond\\")
	h.add("café")
	e := newEditBuffer(100, h)
	e.insert("held")
	e.key("\x1b[A")
	e.key("\x1b[D")
	e.key("\x1b[3~")
	e.key("é")
	if string(e.text) != "café" {
		t.Fatalf("edited Unicode recall: %q", string(e.text))
	}
	e.key("\x1b[A")
	if string(e.text) != "first\nsecond\\" || !e.literal {
		t.Fatalf("multiline recall lost content: %#v", e)
	}
	e.key("\x1b[B")
	e.key("\x1b[B")
	if string(e.text) != "held" {
		t.Fatalf("lost pre-history draft: %q", string(e.text))
	}
	e.key("\x01")
	e.key("\x1b[C")
	e.key("\x7f")
	e.key("H")
	e.key("\x05")
	e.key("!")
	if string(e.text) != "Held!" {
		t.Fatalf("cursor edits: %q", string(e.text))
	}
}

func TestEditViewportAllocationsDoNotScaleWithDraft(t *testing.T) {
	for _, size := range []int{1000, 10000, 100000} {
		e := newEditBuffer(size, nil)
		e.insert(strings.Repeat("a", size))
		allocs := testing.AllocsPerRun(5, func() { e.view("> ", 80) })
		// A generous ceiling for the one-row viewport; measuring offscreen
		// characters must never allocate once per character in the draft.
		if allocs > 100 {
			t.Fatalf("%d-rune draft: %.0f redraw allocations", size, allocs)
		}
	}
}

func BenchmarkEditAppendAndRedraw(b *testing.B) {
	for _, size := range []int{1000, 10000, 30000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				e := newEditBuffer(size, nil)
				for i := 0; i < size; i++ {
					e.insert("a")
					e.view("> ", 80)
				}
			}
		})
	}
}

func TestEditBufferSearchRequiresAcceptance(t *testing.T) {
	h := &replHistory{}
	h.add("older match")
	h.add("unrelated")
	h.add("newer match")
	e := newEditBuffer(100, h)
	e.insert("unsent")
	e.key("\x12")
	for _, r := range "match" {
		e.key(string(r))
	}
	if !e.searching || string(e.text) != "newer match" {
		t.Fatalf("search=%#v", e)
	}
	e.key("\x12")
	if string(e.text) != "older match" {
		t.Fatalf("repeat search=%q", string(e.text))
	}
	e.key("\n")
	if e.searching || string(e.text) != "older match" {
		t.Fatal("Enter must accept for editing")
	}
	e.key("\x12")
	e.key("\x1b")
	if e.searching || string(e.text) != "older match" {
		t.Fatal("escape must restore the pre-search draft")
	}
}

func TestEditBufferPastedSearch(t *testing.T) {
	h := &replHistory{}
	h.add("older needle\n界\\")
	h.add("newer needle\n界\\")
	h.add("unrelated")
	e := newEditBuffer(100, h)
	e.paste("unsent\ndraft\\", false)
	e.key("\x1b[D")
	e.key("\x12")
	e.paste("needle\n界\\", false)
	if string(e.query) != "needle\n界\\" || string(e.text) != "newer needle\n界\\" || !e.searching {
		t.Fatalf("paste changed the candidate instead of searching: %#v", e)
	}
	e.key("\x12")
	if string(e.text) != "older needle\n界\\" {
		t.Fatalf("repeat pasted search: %q", string(e.text))
	}
	e.paste("missing", false)
	if e.searchAt != -1 || string(e.text) != "unsent\ndraft\\" || e.cursor != 12 || !e.literal {
		t.Fatalf("no match lost the original draft: %#v", e)
	}
	e.key("\x1b")
	if e.searching || string(e.text) != "unsent\ndraft\\" || e.cursor != 12 || !e.literal {
		t.Fatalf("Escape lost the original draft: %#v", e)
	}
}

func TestEditBufferSearchQueryHasItsOwnByteLimit(t *testing.T) {
	h := &replHistory{}
	h.add("long candidate é界")
	e := newEditBuffer(6, h)
	e.insert("draft")
	e.key("\x12")
	if e.pasteLimit() != 6 {
		t.Fatal("candidate size limited the query")
	}
	e.key("é")
	e.paste("界", false)
	if e.queryBytes != 5 || e.pasteLimit() != 1 || string(e.query) != "é界" {
		t.Fatalf("query limit does not count UTF-8 bytes: %#v", e)
	}
	for _, paste := range []struct {
		text string
		long bool
	}{{"é", false}, {"x", true}} {
		e.paste(paste.text, paste.long)
		if string(e.query) != "é界" || e.queryBytes != 5 || string(e.text) != "long candidate é界" || !e.queryRejected {
			t.Fatalf("oversize query was partially applied: %#v", e)
		}
		if view, _ := e.view("> ", 80); !strings.Contains(view, "previous query kept") {
			t.Fatalf("query rejection is invisible: %q", view)
		}
	}
	e.key("\x7f")
	if string(e.query) != "é" || e.queryBytes != 2 || e.queryRejected {
		t.Fatal("backspace failed to recover the search")
	}
	e.paste("none", false)
	if e.searchAt != -1 || string(e.text) != "draft" || e.tooLong {
		t.Fatal("no match inherited the candidate's overflow state")
	}
	e.key("\x1b")
	if string(e.text) != "draft" || e.cursor != 5 || e.literal || e.tooLong {
		t.Fatal("search changed the original draft")
	}
}

func TestEditBufferRecallAndSearchPreserveRejectedDraft(t *testing.T) {
	for _, keys := range [][]string{{"\x1b[A", "\x1b[B"}, {"\x12", "\x1b"}, {"\x12", "none", "\n"}} {
		h := &replHistory{}
		h.add("saved")
		e := newEditBuffer(5, h)
		e.insert("draft")
		e.insert("overflow\\")
		e.key("\x1b[D")
		for _, key := range keys {
			e.key(key)
		}
		if string(e.text) != "draft" || e.cursor != 4 || !e.tooLong {
			t.Fatalf("%q made a rejected prefix submittable: %#v", keys, e)
		}
		if line := e.inputLine(false); line.text != "" || line.tail != "ow\\" || !line.tooLong || line.literal {
			t.Fatalf("%q lost rejected continuation state: %+v", keys, line)
		}
	}
}

func TestEditBufferRejectedTailAndExplicitRecovery(t *testing.T) {
	e := newEditBuffer(4, nil)
	e.insert("abc\\")
	e.insert("\\")
	if line := e.inputLine(false); line.text != "" || line.tail != "c\\\\" || !line.tooLong {
		t.Fatalf("overflow lost the boundary-spanning doubled backslash: %+v", line)
	}
	e.insert("界")
	e.insert("\\")
	e.key("\x7f")
	if line := e.inputLine(false); line.text != "" || line.tail != "\x95\x8c\\" || !line.tooLong {
		t.Fatalf("deletion salvaged a rejected prefix or lost its tail: %+v", line)
	}
	e.key("\x15")
	e.insert("yes")
	if line := e.inputLine(false); line.text != "yes" || line.tooLong || line.tail != "" || line.literal {
		t.Fatalf("clear did not recover clean input: %+v", line)
	}
}

func TestEditBufferRejectedInsertionRetainsBothSides(t *testing.T) {
	for _, suffix := range []string{"\\", "\\\\", "界\\"} {
		for _, keys := range [][]string{
			{"\x1b[A", "\x1b[B"}, {"\x12", "\x1b"}, {"\x12", "absent", "\n"},
		} {
			h := &replHistory{}
			h.add("saved")
			e := newEditBuffer(10, h)
			e.insert("prefix" + suffix)
			e.moveCursor(6)
			e.insert("overflow")
			for _, key := range keys {
				e.key(key)
			}
			e.insert("\\")
			line := e.inputLine(false)
			want := "\\\\" // Inserted backslash immediately precedes the suffix.
			if suffix == "界\\" {
				want = "\x95\x8c\\"
			}
			if !line.tooLong || line.text != "" || !strings.HasSuffix(line.tail, want) {
				t.Fatalf("suffix=%q keys=%q lost rejected insertion: %+v", suffix, keys, line)
			}
			if len(e.tail) > lineTailBytes || len(e.tailSuffix) > lineTailBytes {
				t.Fatal("rejected input retained an unbounded tail")
			}
			e.key("\x15")
			e.insert("fresh")
			if line := e.inputLine(false); line.text != "fresh" || line.tooLong || e.tailSuffix != "" {
				t.Fatalf("explicit clear retained rejection state: %+v", line)
			}
		}
	}
}

func TestEditBufferTracksOnlyHistorySlashEscape(t *testing.T) {
	e := newEditBuffer(100, nil)
	e.replaceHistory("/exit")
	e.key("\x12")
	e.key("\x1b")
	if !e.inputLine(false).escapedSlash {
		t.Fatal("search restoration lost history's escape")
	}
	e.moveCursor(0)
	e.paste("// comment ", false)
	if e.inputLine(false).escapedSlash {
		t.Fatal("inserting a new prefix retained a stale history escape")
	}
	e.replaceHistory("/exit")
	e.remove(0, 1)
	e.insert("/")
	if e.inputLine(false).escapedSlash {
		t.Fatal("deleting the synthetic slash retained its escape")
	}
	e.replaceHistory("/exit")
	e.key("\x15")
	e.paste("// comment", false)
	if e.inputLine(false).escapedSlash {
		t.Fatal("clear applied a history escape to pasted content")
	}
}

func TestEditBufferMultilineCursorMetadata(t *testing.T) {
	history := &replHistory{}
	history.add("/older\nprompt")
	e := newEditBuffer(1000, history)
	e.paste("a\n界b\nlast", false)
	for _, key := range []string{
		"\x01", "\x1b[D", "\x1b[A", "\x1b[B", "\x1b[D", "\x7f", "X\nY",
		"\x12", "older", "\x1b", "\x05", "\x1b[3~", "\x17", "\x0b", "\x15", "fresh",
	} {
		e.key(key)
		text := string(e.text)
		line := 1 + strings.Count(string(e.text[:e.cursor]), "\n")
		lines := 1 + strings.Count(text, "\n")
		if e.line != line || e.lines != lines || e.bytes != len(text) {
			t.Fatalf("after %q: text=%q cursor=%d line=%d/%d, want %d/%d", key, text, e.cursor, e.line, e.lines, line, lines)
		}
		view, _ := e.view("> ", 80)
		if lines > 1 && !e.searching && !strings.HasPrefix(view, fmt.Sprintf("[%d/%d]", line, lines)) {
			t.Fatalf("wrong visible line position: %q", view)
		}
	}
	if string(e.text) != "fresh" || e.cursor != 5 {
		t.Fatalf("editing did not recover a fresh line: %#v", e)
	}
}

func TestEditViewportBoundsInvisibleCharacters(t *testing.T) {
	for _, content := range []string{strings.Repeat("\u0301", 100000), strings.Repeat("界\t\x1b\u200d", 10000)} {
		e := newEditBuffer(len(content), nil)
		e.insert(content)
		for _, pos := range []int{0, len(e.text) / 2, len(e.text)} {
			e.moveCursor(pos)
			for _, columns := range []int{5, 8, 20, 80} {
				view, cursor := e.view("> ", columns)
				cells := 0
				for _, r := range view {
					cells += editRuneWidth(r)
				}
				if len(view) > columns*8*4+columns || cells >= columns || cursor >= columns || cursor < 0 || strings.ContainsRune(view, '\x1b') {
					t.Fatalf("unbounded or unsafe viewport: bytes=%d cells=%d cursor=%d columns=%d", len(view), cells, cursor, columns)
				}
			}
		}
		if string(e.text) != content {
			t.Fatal("viewport clipping changed submitted content")
		}
	}
}

func TestHistoryRecallEscapesCommandsAndRestoresDraftSemantics(t *testing.T) {
	h := &replHistory{}
	h.add("/exit")
	e := newEditBuffer(100, h)
	e.insert("unfinished\\")
	e.key("\x1b[A")
	if string(e.text) != "//exit" {
		t.Fatalf("literal history became a command: %q", string(e.text))
	}
	e.key("\x1b[B")
	if string(e.text) != "unfinished\\" || e.literal {
		t.Fatal("history navigation changed the draft's continuation semantics")
	}
	e.key("\x12")
	e.key("\x1b")
	if e.literal || string(e.text) != "unfinished\\" {
		t.Fatal("search cancellation changed the draft")
	}
	e.key("\x1b[A")
	e.key("\x15")
	if e.literal {
		t.Fatal("clearing recalled input must restore typed continuation rules")
	}
}

func TestEditBufferBoundsAndNarrowViewport(t *testing.T) {
	e := newEditBuffer(10, nil)
	e.insert("café")
	e.insert("123456")
	if !e.tooLong || string(e.text) != "café" {
		t.Fatal("oversize insertion must not silently send a prefix")
	}
	e.key("\x15")
	e.insert("abc")
	if e.tooLong || string(e.text) != "abc" {
		t.Fatal("clear must recover from excessive input")
	}
	e = newEditBuffer(1000, nil)
	e.insert(strings.Repeat("界", 30))
	for _, width := range []int{1, 2, 4, 8, 20, 40, 80} {
		view, cursor := e.view("> ", width)
		cells := 0
		for _, r := range view {
			if r == '界' {
				cells += 2
			} else {
				cells++ // The prompt and clipping markers are ASCII.
			}
		}
		if cells >= width || cursor >= width || cursor < 0 {
			t.Fatalf("width=%d view=%q cells=%d cursor=%d", width, view, cells, cursor)
		}
	}
	e.tooLong = true
	if view, cursor := e.view("> ", 8); len(view) >= 8 || cursor >= 8 {
		t.Fatalf("overflow notice exceeds viewport: %q cursor=%d", view, cursor)
	}
}
