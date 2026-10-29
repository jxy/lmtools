package tools

import (
	"lmtools/internal/core"
	"lmtools/internal/termwidth"
	"strings"
	"testing"
)

func columns(n int) func() int { return func() int { return n } }

// rowWidth measures one row of the transcript in terminal cells.
func rowWidth(row string) int {
	cells := 0
	for _, r := range row {
		cells += termwidth.RuneWidth(r)
	}
	return cells
}

// A line longer than the terminal is wrapped by the transcript itself: every
// row fits within the width less one cell, and the rows after the first start
// with the continuation marker, so they stay in their block.
func TestTranscriptWrapsLongLinesWithAContinuationMarker(t *testing.T) {
	notifier := newFormatTestNotifier()
	line := "INFO server started on port 8080; config loaded from /etc/app/config.yaml with overrides"
	writeToolOutput(notifier, "Output", "", line+"\n", Options{Columns: columns(40)})
	// 39 cells a row: the prefix and 35 characters.
	want := "  Output (1 line):\n" +
		"  | INFO server started on port 8080; c\n" +
		"  : onfig loaded from /etc/app/config.y\n" +
		"  : aml with overrides\n"
	if got := notifier.out.String(); got != want {
		t.Fatalf("wrapped output =\n%s\nwant \n%s", got, want)
	}
}

// A wide character that would cross the last column starts the next row, and
// a combining mark stays with the character before it.
func TestTranscriptWrapsWideAndCombiningCharactersWhole(t *testing.T) {
	notifier := newFormatTestNotifier()
	text := strings.Repeat("\u754c", 20) + " e" + "\u0301" + strings.Repeat("x", 30)
	writeToolOutput(notifier, "Output", "", text+"\n", Options{Columns: columns(25)})
	rows := strings.Split(strings.TrimSuffix(notifier.out.String(), "\n"), "\n")
	var content strings.Builder
	for i, row := range rows {
		if w := rowWidth(row); w > 24 {
			t.Fatalf("row %d is %d cells wide: %q", i, w, row)
		}
		switch i {
		case 0:
		case 1:
			content.WriteString(strings.TrimPrefix(row, "  | "))
		default:
			if !strings.HasPrefix(row, ContinuationPrefix) {
				t.Fatalf("row %d does not continue the line: %q", i, row)
			}
			if strings.HasPrefix(strings.TrimPrefix(row, ContinuationPrefix), "\u0301") {
				t.Fatalf("row %d starts with a combining mark", i)
			}
			content.WriteString(strings.TrimPrefix(row, ContinuationPrefix))
		}
	}
	if content.String() != text {
		t.Fatalf("rows joined = %q, want %q", content.String(), text)
	}
}

// Each line is measured from its own start, so text after a newline wraps
// where it would alone.
func TestWrapMeasuresEachLine(t *testing.T) {
	line := strings.Repeat("x", 30)
	want := "\n" + line[:20] + "\n" + ContinuationPrefix + line[20:]
	if got := Wrap("\n"+line, columns(21)); got != want {
		t.Fatalf("Wrap = %q, want %q", got, want)
	}
}

// Tabs in quoted content reach the next multiple of eight columns counted
// from the content's start, so a table keeps its alignment under the quote.
func TestTranscriptExpandsTabsFromTheContentStart(t *testing.T) {
	notifier := newFormatTestNotifier()
	writeToolOutput(notifier, "Output", "", "a\tb\nlonger\tc\n", Options{})
	want := "  Output (2 lines):\n  | a       b\n  | longer  c\n"
	if got := notifier.out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// Review lines and result lines wrap the same way as output, and a terminal
// too narrow to help, or no known width, leaves lines whole.
func TestTranscriptWrapsReviewAndResultLines(t *testing.T) {
	call := commandCall(t, "c1", []string{"printf", "%s", strings.Repeat("y", 120)}, core.UniversalCommandArgs{})
	result := core.ToolResult{ID: "c1", Error: "exit status 1: " + strings.Repeat("z", 60), Elapsed: 5}
	for _, tt := range []struct {
		width   int
		wrapped bool
	}{
		{width: 50, wrapped: true},
		{width: 12, wrapped: false},
		{width: 0, wrapped: false},
	} {
		notifier := newFormatTestNotifier()
		options := Options{}
		if tt.width > 0 {
			options.Columns = columns(tt.width)
		}
		ui := NewCLIToolUIWithOptions(notifier, options)
		ui.ShowCall(0, 1, call, nil)
		ui.AfterExecute([]core.ToolCall{call}, []core.ToolResult{result})
		got := notifier.out.String()
		continued := strings.Contains(got, "\n"+ContinuationPrefix)
		if continued != tt.wrapped {
			t.Fatalf("width %d: wrapped %v:\n%s", tt.width, continued, got)
		}
		if tt.wrapped {
			for _, row := range strings.Split(got, "\n") {
				if rowWidth(row) > tt.width-1 {
					t.Fatalf("width %d: row %q is too wide", tt.width, row)
				}
			}
		}
	}
}

// A value under review shows its tabs: a script indented with tabs and one
// indented with spaces must not look alike, which matters for Makefiles, TSV
// input, and Python. Tool output alone expands tabs for alignment.
func TestApprovalValuesShowTheirTabs(t *testing.T) {
	show := func(script string) string {
		notifier := newFormatTestNotifier()
		call := commandCall(t, "c1", []string{"make", "-f", "-"}, core.UniversalCommandArgs{Stdin: &script})
		NewCLIToolUIWithOptions(notifier, Options{Columns: columns(80)}).ShowCall(0, 1, call, nil)
		return notifier.out.String()
	}
	tabs := show("build:\n\tgo build ./...\n")
	spaces := show("build:\n        go build ./...\n")
	if tabs == spaces {
		t.Fatalf("a tab and eight spaces look alike:\n%s", tabs)
	}
	for _, want := range []string{
		"  Tabs and control characters are shown as \\uXXXX.\n",
		"  | \\u0009go build ./...\n",
	} {
		if !strings.Contains(tabs, want) {
			t.Fatalf("missing %q in:\n%s", want, tabs)
		}
	}
	if strings.Contains(tabs, "\t") || strings.Contains(spaces, "shown as") {
		t.Fatalf("tabs %q; spaces %q", tabs, spaces)
	}
}
