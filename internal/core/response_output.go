package core

import (
	"fmt"
	"io"
)

// ResponseStreamEventType identifies a provider-neutral piece of a streamed
// response. Provider parsers may expose visible reasoning through these events
// without mixing it into the assistant answer text.
type ResponseStreamEventType string

const (
	ResponseStreamTextDelta      ResponseStreamEventType = "text_delta"
	ResponseStreamReasoningStart ResponseStreamEventType = "reasoning_start"
	ResponseStreamReasoningDelta ResponseStreamEventType = "reasoning_delta"
	ResponseStreamReasoningEnd   ResponseStreamEventType = "reasoning_end"
	// ResponseStreamToolCallStart marks the model beginning a tool call. Text
	// carries the tool name; the arguments follow silently until the call is
	// complete, which is the gap a presenter can explain to the operator.
	ResponseStreamToolCallStart ResponseStreamEventType = "tool_call_start"
)

// ResponseStreamEvent is a semantic output event produced while parsing a
// provider stream. ReasoningType contains the provider block type on start/end
// events; Text is populated for visible text and reasoning deltas and names the
// tool on a tool call start.
type ResponseStreamEvent struct {
	Type          ResponseStreamEventType
	Text          string
	ReasoningType string
	// Incomplete marks a reasoning block cut short by a provider limit.
	Incomplete bool
}

// ResponseOutput receives live response events and the completed parsed
// response. Implementations decide how to present answer text and visible
// reasoning; opaque signatures and encrypted payloads are never stream events.
type ResponseOutput interface {
	HandleStreamEvent(ResponseStreamEvent)
	HandleResponseComplete(Response)
}

type textResponseOutput struct {
	out io.Writer
}

func (o textResponseOutput) HandleStreamEvent(event ResponseStreamEvent) {
	if event.Type == ResponseStreamTextDelta && event.Text != "" {
		_, _ = fmt.Fprint(o.out, event.Text)
	}
}

func (textResponseOutput) HandleResponseComplete(Response) {}

func responseOutputOrDefault(output ResponseOutput, out io.Writer) ResponseOutput {
	if output != nil {
		return output
	}
	return textResponseOutput{out: out}
}

type responseOutputWriter struct {
	output ResponseOutput
}

func (w responseOutputWriter) Write(data []byte) (int, error) {
	if len(data) > 0 {
		w.output.HandleStreamEvent(ResponseStreamEvent{
			Type: ResponseStreamTextDelta,
			Text: string(data),
		})
	}
	return len(data), nil
}

// ReasoningTextForDisplay returns only provider-visible reasoning text. It
// intentionally excludes signatures, encrypted content, and opaque redacted
// data while accepting the summary/content shapes used by compatible APIs.
func ReasoningTextForDisplay(block ReasoningBlock) string {
	if block.Text != "" {
		return block.Text
	}
	return foreignReasoningSummaryText(block)
}
