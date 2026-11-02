package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	cliui "lmtools/internal/ui"
	toolui "lmtools/internal/ui/tools"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsePresenterDisabledPreservesAnswerOnlyOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, false)

	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart, ReasoningType: "thinking"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "private-looking summary"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningEnd, ReasoningType: "thinking"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: "answer"})
	presenter.HandleResponseComplete(core.Response{Streamed: true})
	presenter.Close()

	if got := stdout.String(); got != "answer" {
		t.Fatalf("stdout = %q, want answer only", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want no thinking output by default", got)
	}
}

func TestResponsePresenterStreamsThinkingSeparately(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, true)

	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart, ReasoningType: "thinking"})
	if got, want := stderr.String(), "--- thinking ---\n"; got != want {
		t.Fatalf("opening marker was not emitted at stream start: got %q, want %q", got, want)
	}
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "Check "})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "the facts."})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningEnd, ReasoningType: "thinking"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: "Final"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: " answer"})
	presenter.HandleResponseComplete(core.Response{Streamed: true, Blocks: []core.Block{
		core.ReasoningBlock{Provider: "anthropic", Type: "thinking", Text: "Check the facts.", Signature: "sig"},
	}})
	presenter.Close()

	if got := stdout.String(); got != "Final answer" {
		t.Fatalf("stdout = %q, want streamed answer only", got)
	}
	if got, want := stderr.String(), "--- thinking ---\nCheck the facts.\n--- end thinking ---\n\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestResponsePresenterFallsBackToCompletedReasoningForStreamsWithoutReasoningEvents(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, true)
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: "answer"})
	presenter.HandleResponseComplete(core.Response{Streamed: true, Blocks: []core.Block{
		core.ReasoningBlock{
			Provider: "openai",
			Type:     "reasoning",
			Summary:  json.RawMessage(`[{"type":"summary_text","text":"Late summary."}]`),
		},
	}})

	if got := stdout.String(); got != "answer" {
		t.Fatalf("stdout = %q, want answer", got)
	}
	if got, want := stderr.String(), "\n\n--- thinking ---\nLate summary.\n--- end thinking ---\n\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestResponsePresenterWritesNonStreamingAnswer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, false)
	presenter.HandleResponseComplete(core.Response{Text: "non-streamed answer"})
	presenter.Close()

	if got, want := stdout.String(), "non-streamed answer"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want no diagnostics", got)
	}
}

// The note is for reasoning the provider counted and did not show. A response
// without reasoning tokens, which adaptive models give to easy questions, gets
// no note.
func TestResponsePresenterReportsMissingVisibleThinking(t *testing.T) {
	reasoning := 120
	for _, tt := range []struct {
		name  string
		usage *core.Usage
		repl  bool
		want  string
	}{
		{name: "counted and hidden", usage: &core.Usage{ReasoningTokens: &reasoning}, want: "\n\nNote: No visible thinking was returned for 120 reasoning tokens.\n"},
		{name: "repl notes", usage: &core.Usage{ReasoningTokens: &reasoning}, repl: true, want: "\n\nNo visible thinking was returned for 120 reasoning tokens.\n"},
		{name: "no reasoning", usage: &core.Usage{}, want: "\n\n"},
		{name: "no usage", usage: nil, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			presenter := newResponsePresenter(&stdout, &stderr, true)
			// The note goes through the run's notifier, which writes to the
			// same stream and owns the prefix.
			presenter.notifier = cliui.NewNotifierWithWriter(&stderr)
			if tt.repl {
				presenter.notifier = cliui.NewREPLNotifier(&stderr)
			}
			presenter.HandleResponseComplete(core.Response{Text: "answer", Usage: tt.usage})
			if got, want := stdout.String(), "answer"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if got := stderr.String(); got != tt.want {
				t.Fatalf("stderr = %q, want %q", got, tt.want)
			}
		})
	}
}

// A provider that hides its thinking hides it on every response, so the note
// is given once: for the turn's tool rounds, which share a presenter, and for
// the run's later turns, whose presenters share the run's record.
func TestResponsePresenterNotesMissingVisibleThinkingOnce(t *testing.T) {
	reasoning := 120
	response := core.Response{Text: "answer", Usage: &core.Usage{ReasoningTokens: &reasoning}}
	var noted bool
	var stderr bytes.Buffer
	for turn := 0; turn < 2; turn++ {
		presenter := newResponsePresenter(io.Discard, &stderr, true)
		presenter.notifier = cliui.NewNotifierWithWriter(&stderr)
		presenter.hiddenThinkingNoted = &noted
		presenter.HandleResponseComplete(response)
		presenter.HandleResponseComplete(response)
	}
	if got := strings.Count(stderr.String(), "No visible thinking"); got != 1 {
		t.Fatalf("notes = %d, want 1:\n%s", got, stderr.String())
	}
}

func TestResponsePresenterUsesFinalCompletionForThinkingMarker(t *testing.T) {
	for _, reason := range []string{"", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			presenter := newResponsePresenter(&stdout, &stderr, true)
			presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart})
			presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "Partial thought"})
			presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningEnd})
			presenter.HandleResponseComplete(core.Response{Streamed: true, IncompleteReason: reason})
			presenter.Close()
			marker := thinkingEndMarker
			if reason != "" {
				marker = thinkingIncompleteEndMarker
			}
			if want := thinkingStartMarker + "\nPartial thought\n" + marker + "\n\n"; stderr.String() != want {
				t.Fatalf("thinking = %q, want %q", stderr.String(), want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("thinking reached stdout: %q", stdout.String())
			}
		})
	}
}

func TestResponsePresenterLabelsEachAnswerWithoutChangingStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, true)
	presenter.answerLabel = "Assistant 2"
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "Check"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningEnd})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: "First"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: " part"})
	presenter.HandleResponseComplete(core.Response{Streamed: true, ToolCalls: []core.ToolCall{{ID: "call_1"}}})
	// The same presenter handles the answer after the tool round.
	presenter.HandleResponseComplete(core.Response{Text: "Last"})
	presenter.Close()
	if stdout.String() != "First partLast" {
		t.Fatalf("model answer bytes changed: %q", stdout.String())
	}
	if strings.Count(stderr.String(), "Assistant 2:\n") != 2 {
		t.Fatalf("want one label per response on diagnostics: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), thinkingEndMarker+"\n\nAssistant 2:\n") {
		t.Fatalf("answer label did not follow thinking: %q", stderr.String())
	}
}

func TestResponsePresenterSeparatesDiagnosticsWithoutChangingAnswer(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "no newline", text: "answer", want: "\n\n"},
		{name: "one newline", text: "answer\n", want: "\n"},
		{name: "blank line", text: "answer\n\n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			presenter := newResponsePresenter(&stdout, &stderr, false)
			presenter.HandleResponseComplete(core.Response{
				Text:      tt.text,
				ToolCalls: []core.ToolCall{{ID: "call_1"}},
			})

			if got := stdout.String(); got != tt.text {
				t.Fatalf("stdout = %q, want original answer %q", got, tt.text)
			}
			if got := stderr.String(); got != tt.want {
				t.Fatalf("stderr separator = %q, want %q", got, tt.want)
			}
		})
	}
}

// On a stdout of its own, an empty line keeps the answer of each response off
// the next, as the tool transcript between them does on a shared screen. A
// response without text adds nothing.
func TestResponsePresenterSeparatesTheAnswersOfATurn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&separatedAnswers{out: &stdout}, &stderr, false)
	presenter.separate = false
	calls := []core.ToolCall{{ID: "call_1"}}
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: "Running the tool."})
	presenter.HandleResponseComplete(core.Response{Streamed: true, ToolCalls: calls})
	presenter.HandleResponseComplete(core.Response{ToolCalls: calls})
	presenter.HandleResponseComplete(core.Response{Text: "Line one\n", ToolCalls: calls})
	presenter.HandleResponseComplete(core.Response{Text: "Done."})
	presenter.Close()

	if got, want := stdout.String(), "Running the tool.\n\nLine one\n\nDone."; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want nothing, since the answers are not there", got)
	}
}

func TestResponsePresenterAndToolUIProduceCompactCombinedTranscript(t *testing.T) {
	var transcript bytes.Buffer
	notifier := cliui.NewNotifierWithWriter(&transcript)
	presenter := newResponsePresenter(&transcript, &transcript, false)
	tools := toolui.NewCLIToolUI(notifier)
	call := core.ToolCall{
		ID:   "call_date",
		Name: core.UniversalCommandToolName,
		Args: json.RawMessage(`{"command":["date"]}`),
	}

	presenter.HandleResponseComplete(core.Response{
		Text:      "I'll check.",
		ToolCalls: []core.ToolCall{call},
		Usage:     &core.Usage{},
	})
	notifier.Promptf("Tokens: input 10 | output 3\n")
	tools.ShowCall(0, 1, call, nil)
	tools.BeforeRun(1, 1, 1)
	tools.AfterExecute([]core.ToolCall{call}, []core.ToolResult{{
		ID:      "call_date",
		Output:  "Wed Aug 26\n",
		Elapsed: 3,
	}})
	presenter.HandleResponseComplete(core.Response{Text: "Final answer.", Usage: &core.Usage{}})
	notifier.Promptf("Tokens: input 20 | output 4\n")

	want := `I'll check.

Tokens: input 10 | output 3

Tools: 1
[1/1] Command: ["date"]

Running 1 tool...

Tool results:
[1/1] date: Completed in 3ms
  Output (stdout+stderr, 1 line):
  | Wed Aug 26

Final answer.

Tokens: input 20 | output 4
`
	if got := transcript.String(); got != want {
		t.Fatalf("combined transcript mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestResponsePresenterRendersCompletedReasoningWithoutOpaqueSidecars(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, true)
	presenter.HandleResponseComplete(core.Response{Blocks: []core.Block{
		core.ReasoningBlock{
			Provider:  "anthropic",
			Type:      "thinking",
			Text:      "First summary.",
			Signature: "sig_must_not_print",
		},
		core.TextBlock{Text: "answer"},
		core.ReasoningBlock{
			Provider: "anthropic",
			Type:     "redacted_thinking",
			Raw:      json.RawMessage(`{"type":"redacted_thinking","data":"opaque_must_not_print"}`),
		},
		core.ReasoningBlock{
			Provider:  "google",
			Type:      "thought_signature",
			Signature: "google_must_not_print",
		},
	}})

	if got := stdout.String(); got != "" {
		t.Fatalf("presenter wrote non-stream response text to stdout: %q", got)
	}
	want := "--- thinking ---\nFirst summary.\n--- end thinking ---\n\n--- thinking ---\n[omitted by provider]\n--- end thinking ---\n\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	for _, opaque := range []string{"sig_must_not_print", "opaque_must_not_print", "google_must_not_print"} {
		if strings.Contains(stderr.String(), opaque) {
			t.Fatalf("stderr exposed opaque reasoning sidecar %q: %q", opaque, stderr.String())
		}
	}
}

func TestResponsePresenterMarksSignatureOnlyThinkingAsOmitted(t *testing.T) {
	var stderr bytes.Buffer
	presenter := newResponsePresenter(&bytes.Buffer{}, &stderr, true)
	presenter.HandleResponseComplete(core.Response{Blocks: []core.Block{
		core.ReasoningBlock{
			Provider:  "anthropic",
			Type:      "thinking",
			Signature: "sig_only_must_not_print",
		},
	}})

	if got, want := stderr.String(), "--- thinking ---\n[omitted by provider]\n--- end thinking ---\n\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "sig_only_must_not_print") {
		t.Fatalf("stderr exposed signature-only reasoning: %q", stderr.String())
	}
}

// responsesReasoning is a Responses reasoning item with summary, or without
// one when summary is empty.
func responsesReasoning(summary string) core.ReasoningBlock {
	block := core.ReasoningBlock{Provider: "openai", Type: "reasoning", EncryptedContent: "encrypted_must_not_print", Summary: json.RawMessage(`[]`)}
	if summary != "" {
		block.Summary = json.RawMessage(fmt.Sprintf(`[{"type":"summary_text","text":%q}]`, summary))
	}
	return block
}

// Consecutive Responses reasoning items share one section, as they do when
// streamed: an item without a summary adds nothing to a section with text,
// and a run of them shows one omitted section.
func TestResponsePresenterJoinsConsecutiveResponsesReasoning(t *testing.T) {
	for _, tt := range []struct {
		name   string
		blocks []core.Block
		want   string
	}{
		{
			name:   "summaries",
			blocks: []core.Block{responsesReasoning(""), responsesReasoning("Checked the inputs."), responsesReasoning(""), responsesReasoning("Wrote the answer.")},
			want:   "--- thinking ---\nChecked the inputs.\nWrote the answer.\n--- end thinking ---\n\n",
		},
		{
			name:   "no summaries",
			blocks: []core.Block{responsesReasoning(""), responsesReasoning(""), responsesReasoning("")},
			want:   "--- thinking ---\n[omitted by provider]\n--- end thinking ---\n\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			presenter := newResponsePresenter(&stdout, &stderr, true)
			presenter.HandleResponseComplete(core.Response{Text: "answer", Blocks: append(tt.blocks, core.TextBlock{Text: "answer"})})
			if stderr.String() != tt.want || stdout.String() != "answer" {
				t.Fatalf("stderr = %q, stdout = %q; want %q and the answer", stderr.String(), stdout.String(), tt.want)
			}
		})
	}
}

// responsesReasoningStream streams items reasoning items without a summary,
// then answer, as Argo did for a pro mode request on 2026-10-30.
func responsesReasoningStream(items int, answer string) string {
	var b strings.Builder
	var output []string
	for i := 0; i < items; i++ {
		item := fmt.Sprintf(`{"id":"rs_%d","type":"reasoning","summary":[],"encrypted_content":"enc"}`, i)
		fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":{\"id\":\"rs_%d\",\"type\":\"reasoning\",\"summary\":[]}}\n\n", i, i)
		fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", i, item)
		output = append(output, item)
	}
	message := fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}`, answer)
	fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n", items)
	fmt.Fprintf(&b, "data: {\"type\":\"response.output_text.delta\",\"output_index\":%d,\"content_index\":0,\"delta\":%q}\n\n", items, answer)
	fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", items, message)
	output = append(output, message)
	fmt.Fprintf(&b, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[%s],\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"reasoning_tokens\":7},\"total_tokens\":14}}}\n\n", strings.Join(output, ","))
	return b.String()
}

// streamedResponsesScreen runs a turn whose Responses stream is stream, with
// thinking shown, and returns what the answer and the thinking put on one
// screen.
func streamedResponsesScreen(t *testing.T, stream string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()
	te := newTurnTestEnvAt(t, server.URL, responsesOn, streamOn, thinkingOn)
	var screen bytes.Buffer
	te.env.stdout, te.env.stderr, te.env.answersOnScreen = &screen, &screen, true
	if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "think"}); err != nil {
		t.Fatal(err)
	}
	return screen.String()
}

// A streamed Responses answer shows reasoning items without a summary where
// they happened: one omitted section before the answer.
func TestStreamedResponsesShowReasoningBeforeTheAnswer(t *testing.T) {
	screen := streamedResponsesScreen(t, responsesReasoningStream(3, "DONE_OK"))
	want := thinkingStartMarker + "\n" + omittedThinkingText + "\n" + thinkingEndMarker + "\n\nDONE_OK"
	if !strings.HasPrefix(screen, want) || strings.Count(screen, thinkingStartMarker) != 1 {
		t.Fatalf("screen = %q, want it to start with %q", screen, want)
	}
}

// A summary that only the terminal snapshot carries still shows, after the
// answer, when the item's done event left it out.
func TestStreamedResponsesShowASummaryFromTheTerminalSnapshot(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning"}}`,
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"DONE_OK"}`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Late summary."}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"DONE_OK"}]}]}}`,
	}, "\n\n") + "\n\n"
	screen := streamedResponsesScreen(t, stream)
	if !strings.Contains(screen, "DONE_OK\n\n"+thinkingStartMarker+"\nLate summary.\n"+thinkingEndMarker) {
		t.Fatalf("screen = %q, want the late summary after the answer", screen)
	}
}

func TestResponsePresenterRendersOpenAIReasoningSummary(t *testing.T) {
	var stderr bytes.Buffer
	presenter := newResponsePresenter(&bytes.Buffer{}, &stderr, true)
	presenter.HandleResponseComplete(core.Response{Blocks: []core.Block{
		core.ReasoningBlock{
			Provider:         "openai",
			Type:             "reasoning",
			Summary:          json.RawMessage(`[{"type":"summary_text","text":"Checked the inputs."}]`),
			EncryptedContent: "encrypted_must_not_print",
		},
	}})

	if got, want := stderr.String(), "--- thinking ---\nChecked the inputs.\n--- end thinking ---\n\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "encrypted_must_not_print") {
		t.Fatalf("stderr exposed encrypted reasoning: %q", stderr.String())
	}
}

func TestResponsePresenterCloseTerminatesInterruptedThinkingSection(t *testing.T) {
	var stderr bytes.Buffer
	presenter := newResponsePresenter(&bytes.Buffer{}, &stderr, true)
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart, ReasoningType: "thinking"})
	presenter.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "Partial summary"})
	presenter.Close()
	presenter.Close()

	if got, want := stderr.String(), "--- thinking ---\nPartial summary\n--- end thinking (incomplete) ---\n\n"; got != want {
		t.Fatalf("stderr = %q, want one closed section %q", got, want)
	}
}
