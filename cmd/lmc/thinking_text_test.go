package main

import (
	"bytes"
	"lmtools/internal/core"
	"strings"
	"testing"
)

func TestThinkingTextQuotesWrapsAndTrimsTrailingBlankLines(t *testing.T) {
	var out bytes.Buffer
	text := thinkingText{out: &out}
	text.reset()
	text.quote, text.escape = thinkingQuote, true
	text.width = func() int { return 24 }
	// Deltas split words and paragraphs the way providers send them.
	for _, delta := range []string{"Rayleigh scattering go", "es as one over lambda", " to the fourth.\n\nSecond paragraph", "\x1b[31m.\n\n"} {
		text.write(delta)
	}
	text.finish()
	want := strings.Join([]string{
		"| Rayleigh scattering",
		"| goes as one over",
		"| lambda to the fourth.",
		"|",
		"| Second",
		"| paragraph\\u001b[31m.",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("quoted thinking:\n%q\nwant\n%q", out.String(), want)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > 23 && !strings.Contains(line, `\u001b`) {
			t.Fatalf("line %q passes the last column", line)
		}
	}
}

func TestThinkingTextEndsEachLineAsItArrives(t *testing.T) {
	var out bytes.Buffer
	text := thinkingText{out: &out}
	text.reset()
	text.write("First paragraph.\n\n")
	// A finished line is on screen at once, so a status line can follow it;
	// the blank line waits for more text.
	if out.String() != "First paragraph.\n" {
		t.Fatalf("after a paragraph: %q", out.String())
	}
	text.write("Second.")
	text.finish()
	if out.String() != "First paragraph.\n\nSecond.\n" {
		t.Fatalf("plain thinking = %q", out.String())
	}
}

func TestResponsePresenterQuotesThinkingAndClosesItBeforeAToolCall(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := newResponsePresenter(&stdout, &stderr, true)
	p.quoteThinking(func() int { return 80 })
	for _, event := range []core.ResponseStreamEvent{
		{Type: core.ResponseStreamReasoningStart},
		{Type: core.ResponseStreamReasoningDelta, Text: "Plan the call.\n\n"},
		{Type: core.ResponseStreamReasoningEnd},
		{Type: core.ResponseStreamTextDelta, Text: "Running it now."},
		{Type: core.ResponseStreamToolCallStart, Text: "universal_command"},
	} {
		p.HandleStreamEvent(event)
	}
	// The tool call starts after the answer text: the answer line ends on
	// stderr at once, before the arguments stream.
	want := "--- thinking ---\n| Plan the call.\n--- end thinking ---\n\n\n\n"
	if stderr.String() != want || stdout.String() != "Running it now." {
		t.Fatalf("stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
}

func newQuotedThinking(out *bytes.Buffer, width int) *thinkingText {
	text := &thinkingText{out: out}
	text.reset()
	text.quote, text.escape = thinkingQuote, true
	text.width = func() int { return width }
	return text
}

// Text without spaces, as Chinese and Japanese are written, shows as it
// arrives and fills each line: a line may break at any wide character.
func TestThinkingTextShowsAndWrapsWideTextAsItArrives(t *testing.T) {
	var out bytes.Buffer
	text := newQuotedThinking(&out, 20)
	text.write(strings.Repeat("中文", 6))
	if want := "| 中文中文中文中文\n| 中文中文"; out.String() != want {
		t.Fatalf("before the line ends: %q, want %q", out.String(), want)
	}
	text.finish()
	if want := "| 中文中文中文中文\n| 中文中文\n"; out.String() != want {
		t.Fatalf("finished: %q, want %q", out.String(), want)
	}
}

// A word longer than the rest of the line shows once it reaches the line's
// end, and continues on the next line without a gap.
func TestThinkingTextShowsALongWordAsItOverflows(t *testing.T) {
	var out bytes.Buffer
	text := newQuotedThinking(&out, 20)
	text.write("see " + strings.Repeat("a", 30))
	if !strings.Contains(out.String(), "aaaa") {
		t.Fatalf("a word overflowing the line is still held: %q", out.String())
	}
	text.finish()
	want := "| see\n| " + strings.Repeat("a", 17) + "\n| " + strings.Repeat("a", 13) + "\n"
	if out.String() != want {
		t.Fatalf("long word = %q, want %q", out.String(), want)
	}
}

// When a thinking block ends, its last word shows at once, though the end
// marker waits for the outcome of the response.
func TestResponsePresenterShowsTheLastWordWhenThinkingEnds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := newResponsePresenter(&stdout, &stderr, true)
	p.quoteThinking(func() int { return 80 })
	p.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart})
	p.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "Check the logs first."})
	p.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningEnd})
	if want := "--- thinking ---\n| Check the logs first."; stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}
