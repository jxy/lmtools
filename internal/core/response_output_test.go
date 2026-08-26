package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type recordingResponseOutput struct {
	events    []ResponseStreamEvent
	completed []Response
}

type orderedResponseOutput struct {
	order *[]string
}

func (*orderedResponseOutput) HandleStreamEvent(ResponseStreamEvent) {}

func (o *orderedResponseOutput) HandleResponseComplete(Response) {
	*o.order = append(*o.order, "response complete")
}

type orderedNotifier struct {
	order *[]string
}

func (*orderedNotifier) Warnf(string, ...interface{})   {}
func (*orderedNotifier) Errorf(string, ...interface{})  {}
func (*orderedNotifier) Promptf(string, ...interface{}) {}

func (n *orderedNotifier) Infof(format string, args ...interface{}) {
	*n.order = append(*n.order, fmt.Sprintf(format, args...))
}

func TestHandleResponseCompletesPresentationBeforeUsageNote(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}
	var order []string
	_, err := HandleResponseWithOptions(
		context.Background(),
		RequestOptions{Provider: "openai", Model: "gpt-5"},
		response,
		&MockLogger{},
		&orderedNotifier{order: &order},
		ResponseParseOptions{Output: &orderedResponseOutput{order: &order}},
	)
	if err != nil {
		t.Fatalf("HandleResponseWithOptions() error = %v", err)
	}
	want := []string{"response complete", "Token usage: input 3, output 2, total 5"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("event order = %#v, want %#v", order, want)
	}
}

func (o *recordingResponseOutput) HandleStreamEvent(event ResponseStreamEvent) {
	o.events = append(o.events, event)
}

func (o *recordingResponseOutput) HandleResponseComplete(response Response) {
	o.completed = append(o.completed, response)
}

func TestRunStreamEmitsAnthropicThinkingAsSemanticEvents(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"carefully."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_must_not_be_an_event"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}

`

	logFile, err := os.CreateTemp(t.TempDir(), "stream-*.log")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer logFile.Close()

	state := &AnthropicStreamState{}
	output := &recordingResponseOutput{}
	text, calls, err := RunStream(
		context.Background(),
		io.NopCloser(strings.NewReader(stream)),
		logFile,
		output,
		&MockNotifier{},
		state,
		"anthropic",
	)
	if err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	if text != "Answer" || len(calls) != 0 {
		t.Fatalf("RunStream() = text %q, calls %+v; want Answer and no calls", text, calls)
	}

	wantEvents := []ResponseStreamEvent{
		{Type: ResponseStreamReasoningStart, ReasoningType: "thinking"},
		{Type: ResponseStreamReasoningDelta, Text: "Check ", ReasoningType: "thinking"},
		{Type: ResponseStreamReasoningDelta, Text: "carefully.", ReasoningType: "thinking"},
		{Type: ResponseStreamReasoningEnd, ReasoningType: "thinking"},
		{Type: ResponseStreamTextDelta, Text: "Answer"},
	}
	if !reflect.DeepEqual(output.events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", output.events, wantEvents)
	}
	for _, event := range output.events {
		if strings.Contains(event.Text, "sig_must_not_be_an_event") {
			t.Fatalf("signature leaked into stream event: %#v", event)
		}
	}

	blocks := state.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v, want thinking and text", blocks)
	}
	reasoning, ok := blocks[0].(ReasoningBlock)
	if !ok || reasoning.Text != "Check carefully." || reasoning.Signature != "sig_must_not_be_an_event" {
		t.Fatalf("reasoning block = %#v, want preserved text and signature", blocks[0])
	}
}

func TestReasoningTextForDisplayExcludesOpaqueFields(t *testing.T) {
	block := ReasoningBlock{
		Provider:         "openai",
		Type:             "reasoning",
		Summary:          []byte(`[{"type":"summary_text","text":"Visible summary"}]`),
		EncryptedContent: "encrypted_must_not_print",
		Signature:        "signature_must_not_print",
		Raw:              []byte(`{"type":"reasoning","encrypted_content":"raw_secret","summary":[{"text":"Visible summary"}]}`),
	}
	if got := ReasoningTextForDisplay(block); got != "Visible summary" {
		t.Fatalf("ReasoningTextForDisplay() = %q, want visible summary only", got)
	}

	exact := "  provider text\n"
	if got := ReasoningTextForDisplay(ReasoningBlock{Text: exact, Signature: "secret"}); got != exact {
		t.Fatalf("ReasoningTextForDisplay() = %q, want exact block.Text %q", got, exact)
	}
}
