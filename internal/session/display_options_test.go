package session

import (
	"bytes"
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"strings"
	"testing"
)

// buildShowSession saves two turns: a plain answer with visible thinking, and
// a tool round with its results and a final answer.
func buildShowSession(t *testing.T) *Session {
	t.Helper()
	return buildShowSessionWithNote(t, "")
}

// buildShowSessionWithNote saves the results with note as their text, which
// is how a round with truncated output is saved.
func buildShowSessionWithNote(t *testing.T, note string) *Session {
	t.Helper()
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "line one\nline two\nline three\nline four")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	store := NewStore(sess, core.NewTestLogger(false))
	if _, _, err := store.SaveAssistantResponse(ctx, core.Response{
		Text: "first answer",
		Blocks: []core.Block{
			core.ReasoningBlock{Provider: "anthropic", Type: "thinking", Text: "Plan the answer.\n\nThen write it.", Signature: "sig"},
			core.TextBlock{Text: "first answer"},
		},
	}, "test-model"); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, sess, core.RoleUser, "second question")
	call := core.ToolCall{ID: "call-1", Name: "universal_command", Args: json.RawMessage(`{"command":["ls"]}`)}
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant}, []core.ToolCall{call}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: "a.txt\n", Elapsed: 2}}, note); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "second answer")
	return sess
}

func showWith(t *testing.T, sess *Session, options ShowOptions) string {
	t.Helper()
	var out bytes.Buffer
	if err := ShowSessionWithOptions(context.Background(), &out, sess, core.NewTestNotifier(), options); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestShowLabelsToolResultsAndSummarizesTheSystemPrompt(t *testing.T) {
	sess := buildShowSession(t)
	got := showWith(t, sess, ShowOptions{SystemSummary: true})
	if !strings.Contains(got, "[tool results] ") || strings.Count(got, "[user] ") != 2 {
		t.Fatalf("tool results are not labelled as such:\n%s", got)
	}
	if !strings.Contains(got, "(4 lines; /show system prints it)") || strings.Contains(got, "line three") {
		t.Fatalf("system prompt not summarized:\n%s", got)
	}
	if strings.Contains(got, "--- thinking ---") {
		t.Fatalf("thinking shown without being asked for:\n%s", got)
	}
	if full := showWith(t, sess, ShowOptions{}); !strings.Contains(full, "line three") {
		t.Fatalf("full display left out the system prompt:\n%s", full)
	}
	if system := showWith(t, sess, ShowOptions{SystemOnly: true}); !strings.HasSuffix(system, "line one\nline two\nline three\nline four\n") || strings.Contains(system, "first question") {
		t.Fatalf("system display = %q", system)
	}
}

// Results saved with a truncation note are tool results all the same, and
// the last turn still starts at its prompt.
func TestShowTreatsResultsWithANoteAsToolResults(t *testing.T) {
	sess := buildShowSessionWithNote(t, "Output of call-1 was truncated to 10 bytes.")
	got := showWith(t, sess, ShowOptions{SystemSummary: true})
	if !strings.Contains(got, "[tool results] ") || strings.Count(got, "[user] ") != 2 {
		t.Fatalf("results with a note are not labelled as tool results:\n%s", got)
	}
	last := showWith(t, sess, ShowOptions{LastTurns: 1})
	if !strings.Contains(last, "second question") || strings.Contains(last, "first question") {
		t.Fatalf("last turn:\n%s", last)
	}
}

func TestShowLastTurnsStartsAtTheirPrompt(t *testing.T) {
	sess := buildShowSession(t)
	last := showWith(t, sess, ShowOptions{LastTurns: 1})
	if strings.Contains(last, "first question") || !strings.Contains(last, "second question") ||
		!strings.Contains(last, "| a.txt") || !strings.Contains(last, "second answer") || !strings.Contains(last, ", last 1 turn:") {
		t.Fatalf("last turn:\n%s", last)
	}
	// The result names its call, made inside the window.
	if !strings.Contains(last, "[1/1] ls: Completed in 2ms") {
		t.Fatalf("result lost its call:\n%s", last)
	}
	if both := showWith(t, sess, ShowOptions{LastTurns: 5}); !strings.Contains(both, "first question") || strings.Contains(both, ", last") {
		t.Fatalf("more turns than exist should show everything:\n%s", both)
	}
}

func TestShowThinkingQuotesSavedReasoning(t *testing.T) {
	sess := buildShowSession(t)
	got := showWith(t, sess, ShowOptions{Thinking: true})
	want := "--- thinking ---\n| Plan the answer.\n|\n| Then write it.\n--- end thinking ---\nfirst answer\n"
	if !strings.Contains(got, want) {
		t.Fatalf("thinking display:\n%s\nwant it to contain\n%s", got, want)
	}
}

// Consecutive Responses reasoning items share one section, as they did live;
// other blocks keep a section each.
func TestShowThinkingJoinsConsecutiveResponsesReasoning(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	store := NewStore(sess, core.NewTestLogger(false))
	responses := func(summary string) core.ReasoningBlock {
		block := core.ReasoningBlock{Provider: "openai", Type: "reasoning", EncryptedContent: "enc", Summary: json.RawMessage(`[]`)}
		if summary != "" {
			block.Summary = json.RawMessage(`[{"type":"summary_text","text":"` + summary + `"}]`)
		}
		return block
	}
	if _, _, err := store.SaveAssistantResponse(ctx, core.Response{
		Text: "answer",
		Blocks: []core.Block{
			responses(""), responses(""), responses("Checked the inputs."), responses("Wrote the answer."),
			core.TextBlock{Text: "answer"},
			responses(""), responses(""),
		},
	}, "test-model"); err != nil {
		t.Fatal(err)
	}
	got := showWith(t, sess, ShowOptions{Thinking: true})
	want := "--- thinking ---\n| Checked the inputs.\n| Wrote the answer.\n--- end thinking ---\n" +
		"--- thinking ---\n| [omitted by provider]\n--- end thinking ---\nanswer\n"
	if !strings.Contains(got, want) {
		t.Fatalf("thinking display:\n%s\nwant it to contain\n%s", got, want)
	}
}

// Saved tool rounds wrap within the width /show is given, as live rounds do.
func TestShowWrapsToolRoundsWithinColumns(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	call := core.ToolCall{ID: "call-1", Name: "universal_command", Args: json.RawMessage(`{"command":["cat","log"]}`)}
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant}, []core.ToolCall{call}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: strings.Repeat("w", 100) + "\n", Elapsed: 2}}, ""); err != nil {
		t.Fatal(err)
	}
	got := showWith(t, sess, ShowOptions{Columns: func() int { return 40 }})
	if !strings.Contains(got, "\n  : ") {
		t.Fatalf("long output was not wrapped:\n%s", got)
	}
	for _, row := range strings.Split(got, "\n") {
		if len(row) > 39 {
			t.Fatalf("row %q is wider than the terminal", row)
		}
	}
	if whole := showWith(t, sess, ShowOptions{}); strings.Contains(whole, "\n  : ") {
		t.Fatalf("output wrapped without a width:\n%s", whole)
	}
}
