package main

import (
	"strings"
	"testing"
)

func TestEditViewportCharacterWidths(t *testing.T) {
	for _, tt := range []struct {
		text, display string
		cells         int
	}{
		{"。", "。", 2},
		{"\u3000", "\u3000", 2},
		{"「」", "「」", 4},
		{"Ａ", "Ａ", 2},
		{"ｱ", "ｱ", 1},
		{"界", "界", 2},
		{"e\u0301", "e\u0301", 1},
		{"·", "·", 1},
		{"\t", "    ", 4},
		{"\x1b", `\u001b`, 6},
		{"\u200d", `\u200d`, 6},
	} {
		t.Run(tt.text, func(t *testing.T) {
			e := newEditBuffer(100, nil)
			e.insert(tt.text)
			view, cursor := e.view("> ", 80)
			if view != "> "+tt.display || cursor != 2+tt.cells {
				t.Fatalf("view=%q cursor=%d; want %q cursor=%d", view, cursor, "> "+tt.display, 2+tt.cells)
			}
			if string(e.text) != tt.text {
				t.Fatal("rendering changed the submitted text")
			}
		})
	}
}

func TestEditViewportWideCharacterMovementAndClipping(t *testing.T) {
	e := newEditBuffer(100, nil)
	e.insert("A。ｱ界e\u0301")
	for position, want := range []int{2, 3, 5, 6, 8, 9, 9} {
		e.moveCursor(position)
		view, cursor := e.view("> ", 80)
		if view != "> A。ｱ界e\u0301" || cursor != want {
			t.Fatalf("position=%d view=%q cursor=%d; want %d", position, view, cursor, want)
		}
	}
	e.moveCursor(2)
	e.key("\x7f")
	e.insert("「」")
	if view, cursor := e.view("> ", 80); view != "> A「」ｱ界e\u0301" || cursor != 7 {
		t.Fatalf("edited viewport=%q cursor=%d", view, cursor)
	}
	e = newEditBuffer(100, nil)
	e.insert(strings.Repeat("。", 8))
	for _, tt := range []struct {
		columns, position int
		view              string
		cursor            int
	}{
		{8, 0, "> 。。>", 2},
		{8, 1, "> 。。>", 4},
		{8, 2, "> <。>", 5},
		{8, 8, "> <。", 5},
		{5, 8, "> <", 3},
	} {
		e.moveCursor(tt.position)
		view, cursor := e.view("> ", tt.columns)
		if view != tt.view || cursor != tt.cursor {
			t.Fatalf("columns=%d position=%d: view=%q cursor=%d; want %q %d", tt.columns, tt.position, view, cursor, tt.view, tt.cursor)
		}
	}
	if string(e.text) != strings.Repeat("。", 8) {
		t.Fatal("clipping changed the submitted text")
	}
}
