package main

import (
	"bytes"
	"encoding/json"
	"lmtools/internal/core"
	cliui "lmtools/internal/ui"
	toolui "lmtools/internal/ui/tools"
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

func TestResponsePresenterReportsMissingVisibleThinking(t *testing.T) {
	var stdout, stderr bytes.Buffer
	presenter := newResponsePresenter(&stdout, &stderr, true)
	presenter.HandleResponseComplete(core.Response{
		Text:  "answer",
		Usage: &core.Usage{},
	})

	if got, want := stdout.String(), "answer"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "\n\n"+noThinkingSummaryNote; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
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
	notifier.Infof("Token usage: input 10, output 3")
	tools.ShowCall(0, 1, call, nil)
	tools.BeforeRun(1, 1, 1)
	tools.AfterExecute([]core.ToolCall{call}, []core.ToolResult{{
		ID:      "call_date",
		Output:  "Wed Aug 26\n",
		Elapsed: 3,
	}})
	presenter.HandleResponseComplete(core.Response{Text: "Final answer.", Usage: &core.Usage{}})
	notifier.Infof("Token usage: input 20, output 4")

	want := `I'll check.

Note: Token usage: input 10, output 3

>>> Tools requested: 1
[1/1] Command: ["date"]

>>> Running 1 command...

>>> Results:
[1/1] Completed in 3ms
      Output:
Wed Aug 26

Final answer.

Note: Token usage: input 20, output 4
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
