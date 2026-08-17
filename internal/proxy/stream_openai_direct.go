package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"sort"
	"strings"
)

// forwardOpenAICompatibleSSEWithStops relays an upstream chat completion stream.
// expectedChoices is the n the client asked for; the stream owes a finish_reason
// for every one of them, so pass 1 when the request left n unset.
func forwardOpenAICompatibleSSEWithStops(ctx context.Context, w http.ResponseWriter, reader io.Reader, originalModel, requestName string, stops []string, expectedChoices int) error {
	writer, err := NewSSEWriter(w, ctx)
	if err != nil {
		return err
	}
	forwarder := &openAICompatibleStreamStopForwarder{
		ctx:             ctx,
		writer:          writer,
		originalModel:   originalModel,
		requestName:     requestName,
		stops:           nonEmptyStopSequences(stops),
		expectedChoices: max(expectedChoices, 1),
		stoppers:        make(map[int]*stopTextEnforcer),
		choiceFinished:  make(map[int]bool),
	}
	if err := consumeSSERecords(reader, forwarder.handleRecord); err != nil {
		if errors.Is(err, errStopConsumingSSERecords) {
			return nil
		}
		if termErr := forwarder.terminate(err); termErr != nil && downstreamStreamIsLive(ctx) {
			logger.From(ctx).Errorf("Failed to send terminal %s stream marker: %v", requestName, termErr)
		}
		return err
	}
	return forwarder.finish()
}

// requestedChoiceCount reports how many choices the client asked for. OpenAI
// treats an absent n as 1, and a request that asks for fewer than one choice
// still gets the single-choice contract rather than a stream owing nothing.
func requestedChoiceCount(req *OpenAIRequest) int {
	if req == nil || req.N == nil {
		return 1
	}
	return max(*req.N, 1)
}

type openAICompatibleStreamStopForwarder struct {
	ctx           context.Context
	writer        *SSEWriter
	originalModel string
	requestName   string
	stops         []string
	stoppers      map[int]*stopTextEnforcer
	// expectedChoices is the requested n. Choices 0 through n-1 are owed a
	// finish_reason whether or not the upstream ever mentions them, so a choice
	// that is dropped entirely reads as truncation rather than going unnoticed.
	expectedChoices int
	// A key means the choice was seen; its value says whether it finished.
	// Requested choices that never appear remain detectable by comparing the
	// finished in-range count with expectedChoices.
	choiceFinished map[int]bool
	terminated     bool
	sawDone        bool
	wroteAny       bool
}

func (f *openAICompatibleStreamStopForwarder) handleRecord(record sseRecord) error {
	data := record.data()
	if strings.TrimSpace(data) == OpenAIDoneMarker {
		f.sawDone = true
		return f.closeStreamAndStop()
	}
	if f.terminated {
		return nil
	}
	// An upstream error record ends the turn, and it is the only account of the
	// failure the client will get, so its provider error details are preserved.
	// The top-level model is still rewritten to the client's name, just like
	// ordinary chunks; otherwise a terminal error leaks a mapped backend model.
	// It cannot go through the chunk path: OpenAIStreamChunk has no error field,
	// so the record decodes to a chunk with no choices and no usage and
	// chunkHasDeltaPayload drops it.
	if isOpenAIStreamErrorRecord(data) {
		// A turn that ended does not un-end because the upstream had trouble
		// afterwards. Local stop enforcement makes that ordinary rather than exotic:
		// stripOpenAICompatibleStop removes the stop sequences from the upstream
		// request, so the upstream keeps generating past the point the client was
		// told the answer finished, and can fail on text that will never be sent.
		// Same rule as a late reader error — the client holds a complete answer, and
		// an error behind it only invites throwing that answer away.
		if f.allChoicesFinished() {
			return f.closeStreamAndStop()
		}
		// Text the enforcer is still holding was generated before the failure and
		// belongs in front of it, for the same reason terminate flushes first.
		if err := f.writePendingStopTails(); err != nil {
			return err
		}
		data = rewriteOpenAIStreamErrorModel(data, f.originalModel)
		if err := f.writePayload(record.withData(data)); err != nil {
			return err
		}
		// Close the stream here rather than reading on. The client has been told the
		// turn failed; deltas arriving behind that would describe a turn that
		// continued, and a client that stops at the error never sees them anyway. The
		// marker goes out now instead of whenever the upstream gets around to closing.
		return f.closeStreamAndStop()
	}
	if len(f.stops) == 0 {
		rewritten, err := f.rewriteUnparsedChunk(data)
		if err != nil {
			return err
		}
		return f.writePayload(record.withData(rewritten))
	}

	warnUnknownFields(f.ctx, []byte(data), OpenAIStreamChunk{}, f.requestName+" stream chunk")
	var chunk OpenAIStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		rewritten, rewriteErr := f.rewriteUnparsedChunk(data)
		if rewriteErr != nil {
			return rewriteErr
		}
		return f.writePayload(record.withData(rewritten))
	}
	chunk.Model = f.originalModel

	matchedIndexes := make([]int, 0)
	for i := range chunk.Choices {
		index := chunk.Choices[i].Index
		if _, seen := f.choiceFinished[index]; !seen {
			f.choiceFinished[index] = false
		}
		if stopper := f.stoppers[index]; stopper != nil && stopper.Stopped() {
			chunk.Choices[i].Delta = OpenAIDelta{}
			chunk.Choices[i].FinishReason = nil
			continue
		}
		content := chunk.Choices[i].Delta.Content
		if content != nil && *content != "" {
			stopper := f.stopperForChoice(index)
			filtered, didMatch := stopper.Push(*content)
			if didMatch {
				matchedIndexes = append(matchedIndexes, index)
				f.choiceFinished[index] = true
				chunk.Choices[i].FinishReason = nil
			}
			if filtered == "" {
				chunk.Choices[i].Delta.Content = nil
			} else {
				chunk.Choices[i].Delta.Content = &filtered
			}
		}
		if finishReasonEnded(chunk.Choices[i].FinishReason) {
			if err := f.flushStopTail(index); err != nil {
				return err
			}
			f.choiceFinished[index] = true
		}
	}
	if f.chunkHasDeltaPayload(chunk) {
		if err := f.writeChunk(record, data, chunk); err != nil {
			return err
		}
	}
	for _, index := range matchedIndexes {
		if err := f.writeSyntheticStop(index); err != nil {
			return err
		}
	}
	return nil
}

func (f *openAICompatibleStreamStopForwarder) finish() error {
	if f.terminated {
		return nil
	}
	// [DONE] is the upstream's explicit terminal marker. Without it, every
	// requested choice and every extra choice shown to the client must carry a
	// finish_reason.
	if !f.sawDone && !f.allChoicesFinished() {
		return f.terminate(errors.New("one or more choices never reached a finish_reason"))
	}
	return f.closeStream()
}

// allChoicesFinished does work proportional to the choices the upstream sent,
// not the client's requested n. A key with false is an observed extra or
// requested choice that never ended; requested indexes never observed are
// caught by the in-range count.
func (f *openAICompatibleStreamStopForwarder) allChoicesFinished() bool {
	finishedExpected := 0
	for index, finished := range f.choiceFinished {
		if !finished {
			return false
		}
		if index >= 0 && index < f.expectedChoices {
			finishedExpected++
		}
	}
	return finishedExpected == f.expectedChoices
}

func (f *openAICompatibleStreamStopForwarder) closeStream() error {
	if f.terminated {
		return nil
	}
	if err := f.writePendingStopTails(); err != nil {
		return err
	}
	f.terminated = true
	// Terminate any stream the client has already seen bytes from, even when the
	// upstream omitted [DONE]. A missing marker reads as a dropped connection.
	if f.sawDone || f.wroteAny {
		return f.writeData(OpenAIDoneMarker)
	}
	return nil
}

func (f *openAICompatibleStreamStopForwarder) closeStreamAndStop() error {
	if err := f.closeStream(); err != nil {
		return err
	}
	return errStopConsumingSSERecords
}

// terminate closes out a stream that failed before its documented ending, so
// the client sees the failure followed by [DONE] instead of a truncated stream.
// A stream that already said everything it owed gets only [DONE].
func (f *openAICompatibleStreamStopForwarder) terminate(streamErr error) error {
	if !downstreamStreamIsLive(f.ctx) {
		return nil
	}
	if f.terminated {
		return nil
	}
	// Text held back as a possible stop-sequence prefix is real output that the
	// enforcer was still deciding about. Release it before the error rather than
	// after: a client that stops reading at the error would otherwise lose text
	// the proxy went on to send anyway.
	//
	// This runs before the terminal decision. A backend that sends
	// no separate role chunk and whose first delta is entirely a stop prefix — X
	// for stop XYZ — has the enforcer swallow that delta whole, so nothing has
	// reached the client yet even though the proxy is holding real output.
	// Deciding the stream was empty first would drop that text and the terminal
	// marker with it. The same rule covers an upstream that produces no event at
	// all: its accepted 200 still cannot turn into an empty 200 to the client.
	if err := f.writePendingStopTails(); err != nil {
		return err
	}
	// A transport failure after every choice finished did not unfinish the turn.
	// Sending an error there would make the client discard a complete answer.
	if f.allChoicesFinished() {
		return f.closeStream()
	}
	message := "upstream stream ended before the terminal chunk"
	if streamErr != nil {
		message = fmt.Sprintf("upstream stream ended before the terminal chunk: %v", streamErr)
	}
	logger.From(f.ctx).Warnf("%s stream had no terminal marker; sending error chunk and [DONE]: %s", f.requestName, message)
	errorPayload, err := json.Marshal(OpenAIError{Error: OpenAIErrorDetail{Type: ErrTypeServer, Message: message}})
	if err != nil {
		return err
	}
	if err := f.writeData(string(errorPayload)); err != nil {
		return err
	}
	return f.closeStream()
}

// isOpenAIStreamErrorRecord reports whether an SSE record is an upstream error
// envelope rather than a completion chunk. Saying yes ends the stream, so the
// test is deliberately narrow: a record carrying choices goes down the normal
// path even if it also has an error, so stop enforcement still runs on its
// content, and an error field that holds no error is not one. Backends send a
// failure or a chunk, not both.
func isOpenAIStreamErrorRecord(data string) bool {
	var record struct {
		Error   interface{}       `json:"error"`
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		return false
	}
	if len(record.Choices) > 0 {
		return false
	}
	// JSON null decodes to a nil interface, and some backends spell "nothing went
	// wrong" as an empty string. Cutting a healthy stream short on either would be
	// worse than the duplicate error this check exists to prevent.
	switch value := record.Error.(type) {
	case nil:
		return false
	case string:
		return value != ""
	}
	return true
}

// rewriteOpenAIStreamErrorModel applies model-alias hiding to a recognized
// upstream error envelope. RawMessage keeps every provider-specific error
// value out of an interface{} round trip, which would lose precision for large
// numeric IDs. Records without a top-level model, and records already naming
// the client model, remain byte-for-byte unchanged.
func rewriteOpenAIStreamErrorModel(data, originalModel string) string {
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		return data
	}
	rawModel, ok := record["model"]
	if !ok {
		return data
	}
	var model string
	if err := json.Unmarshal(rawModel, &model); err == nil && model == originalModel {
		return data
	}
	rewrittenModel, err := json.Marshal(originalModel)
	if err != nil {
		return data
	}
	record["model"] = rewrittenModel
	rewritten, err := json.Marshal(record)
	if err != nil {
		return data
	}
	return string(rewritten)
}

func (f *openAICompatibleStreamStopForwarder) writePendingStopTails() error {
	if len(f.stops) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(f.stoppers))
	for index := range f.stoppers {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		if err := f.flushStopTail(index); err != nil {
			return err
		}
	}
	return nil
}

func (f *openAICompatibleStreamStopForwarder) flushStopTail(index int) error {
	stopper := f.stoppers[index]
	if stopper == nil || stopper.Stopped() || f.choiceFinished[index] {
		return nil
	}
	if tail := stopper.Flush(); tail != "" {
		chunk := OpenAIStreamChunk{Object: "chat.completion.chunk", Model: f.originalModel, Choices: []OpenAIStreamDelta{{Index: index, Delta: OpenAIDelta{Content: &tail}}}}
		return f.writeMarshaledChunk(chunk)
	}
	return nil
}

func (f *openAICompatibleStreamStopForwarder) stopperForChoice(index int) *stopTextEnforcer {
	stopper := f.stoppers[index]
	if stopper == nil {
		stopper = newStopTextEnforcer(f.stops)
		f.stoppers[index] = stopper
	}
	return stopper
}

// rewriteUnparsedChunk rewrites the model on a chunk the stop enforcer did not
// handle, and notes which choices it carried on the way past. With no stop
// sequences configured the enforcer never parses a chunk, so this is the only
// place that signal is seen, and finish needs it to tell a completed turn
// from a truncated one. A raw finishing choice also releases any tail the
// enforcer held before the finish chunk reaches the client.
func (f *openAICompatibleStreamStopForwarder) rewriteUnparsedChunk(data string) (string, error) {
	if strings.TrimSpace(data) == OpenAIDoneMarker {
		return data, nil
	}
	warnUnknownFields(f.ctx, []byte(data), OpenAIStreamChunk{}, f.requestName+" stream chunk")
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		return data, nil
	}
	if rawChoices, ok := record["choices"]; ok {
		var choices []openAIStreamChoiceProgress
		if err := json.Unmarshal(rawChoices, &choices); err == nil {
			if err := f.noteChoiceProgress(choices); err != nil {
				return "", err
			}
		}
	}
	rewrittenModel, err := json.Marshal(f.originalModel)
	if err != nil {
		return data, nil
	}
	record["model"] = rewrittenModel
	updated, err := json.Marshal(record)
	if err != nil {
		return data, nil
	}
	return string(updated), nil
}

// A choice ends when its finish_reason says so, and only then. Backends spell
// "still generating" three ways: the field absent, the field null, and — as some
// send on every ongoing chunk — the empty string. Reading an empty reason as an
// ending marks the choice done on its first delta, so a stream that stops
// mid-answer reaches [DONE] carrying no error and the client banks the fragment
// as the whole reply.
//
// finishReasonEnded answers that for a decoded chunk.
func finishReasonEnded(reason *string) bool {
	return reason != nil && *reason != ""
}

// openAIStreamChoiceProgress is the only part of an otherwise raw provider
// record needed to decide whether the stream completed. Keeping finish_reason
// raw preserves the existing compatibility rule for non-string provider values
// without materializing unrelated extension fields.
type openAIStreamChoiceProgress struct {
	Index        int             `json:"index"`
	FinishReason json.RawMessage `json:"finish_reason"`
}

// rawFinishReasonEnded answers the completion question for a raw choice. A
// value that is neither an empty string nor null is taken at face value:
// whatever the backend meant by it, it is not "still generating".
func rawFinishReasonEnded(reason json.RawMessage) bool {
	reason = bytes.TrimSpace(reason)
	return len(reason) > 0 && !bytes.Equal(reason, []byte("null")) && !bytes.Equal(reason, []byte(`""`))
}

// noteChoiceProgress records which choices the chunk carried and which of them
// ended. A finishing choice must release its held text first: flushStopTail
// deliberately ignores finished choices, and the raw finish chunk is itself a
// terminal boundary for clients that stop reading there.
func (f *openAICompatibleStreamStopForwarder) noteChoiceProgress(choices []openAIStreamChoiceProgress) error {
	for _, choice := range choices {
		// Chunks always carry an index in practice; a missing one decodes as the
		// single choice rather than being dropped, so it is still tracked.
		index := choice.Index
		if _, seen := f.choiceFinished[index]; !seen {
			f.choiceFinished[index] = false
		}
		if rawFinishReasonEnded(choice.FinishReason) {
			if err := f.flushStopTail(index); err != nil {
				return err
			}
			f.choiceFinished[index] = true
		}
	}
	return nil
}

func (f *openAICompatibleStreamStopForwarder) writeChunk(record sseRecord, originalData string, chunk OpenAIStreamChunk) error {
	data, err := patchOpenAIStreamChunkData(originalData, f.originalModel, chunk)
	if err != nil {
		return err
	}
	return f.writePayload(record.withData(data))
}

func patchOpenAIStreamChunkData(originalData, model string, chunk OpenAIStreamChunk) (string, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(originalData), &raw); err != nil {
		data, marshalErr := json.Marshal(chunk)
		return string(data), marshalErr
	}
	raw["model"] = model
	if len(chunk.Choices) > 0 {
		rawChoices, _ := raw["choices"].([]interface{})
		patched := make([]interface{}, len(chunk.Choices))
		for i, choice := range chunk.Choices {
			var rawChoice map[string]interface{}
			if i < len(rawChoices) {
				rawChoice, _ = rawChoices[i].(map[string]interface{})
			}
			if rawChoice == nil {
				rawChoice = map[string]interface{}{}
			}
			rawChoice["index"] = choice.Index
			if choice.FinishReason != nil {
				rawChoice["finish_reason"] = *choice.FinishReason
			} else {
				rawChoice["finish_reason"] = nil
			}
			var rawDelta map[string]interface{}
			if delta, ok := rawChoice["delta"].(map[string]interface{}); ok {
				rawDelta = delta
			} else {
				rawDelta = map[string]interface{}{}
			}
			patchOpenAIStreamDeltaData(rawDelta, choice.Delta)
			rawChoice["delta"] = rawDelta
			patched[i] = rawChoice
		}
		raw["choices"] = patched
	}
	updated, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(updated), nil
}

func patchOpenAIStreamDeltaData(rawDelta map[string]interface{}, delta OpenAIDelta) {
	if delta.Role != nil {
		rawDelta["role"] = delta.Role
	} else {
		delete(rawDelta, "role")
	}
	if delta.ContentNull {
		rawDelta["content"] = nil
	} else if delta.Content != nil {
		rawDelta["content"] = *delta.Content
	} else {
		delete(rawDelta, "content")
	}
	if len(delta.ToolCalls) > 0 {
		patchOpenAIStreamToolCallData(rawDelta, delta.ToolCalls)
	} else {
		delete(rawDelta, "tool_calls")
	}
	if delta.FunctionCall != nil {
		rawDelta["function_call"] = delta.FunctionCall
	} else {
		delete(rawDelta, "function_call")
	}
	if delta.Refusal != nil {
		rawDelta["refusal"] = *delta.Refusal
	} else {
		delete(rawDelta, "refusal")
	}
	if delta.Audio != nil {
		rawDelta["audio"] = delta.Audio
	} else {
		delete(rawDelta, "audio")
	}
}

func patchOpenAIStreamToolCallData(rawDelta map[string]interface{}, toolCalls []ToolCallDelta) {
	rawToolCalls, _ := rawDelta["tool_calls"].([]interface{})
	patched := make([]interface{}, len(toolCalls))
	for i, toolCall := range toolCalls {
		var rawToolCall map[string]interface{}
		if i < len(rawToolCalls) {
			rawToolCall, _ = rawToolCalls[i].(map[string]interface{})
		}
		if rawToolCall == nil {
			rawToolCall = map[string]interface{}{}
		}
		rawToolCall["index"] = toolCall.Index
		if toolCall.ID != "" {
			rawToolCall["id"] = toolCall.ID
		} else {
			delete(rawToolCall, "id")
		}
		if toolCall.Type != "" {
			rawToolCall["type"] = toolCall.Type
		} else {
			delete(rawToolCall, "type")
		}
		if toolCall.Function != nil {
			rawToolCall["function"] = toolCall.Function
		} else {
			delete(rawToolCall, "function")
		}
		if toolCall.Custom != nil {
			rawToolCall["custom"] = toolCall.Custom
		} else {
			delete(rawToolCall, "custom")
		}
		patched[i] = rawToolCall
	}
	rawDelta["tool_calls"] = patched
}

func (f *openAICompatibleStreamStopForwarder) writeMarshaledChunk(chunk OpenAIStreamChunk) error {
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	return f.writeData(string(data))
}

func (f *openAICompatibleStreamStopForwarder) writeSyntheticStop(matchedIndex int) error {
	chunk := OpenAIStreamChunk{
		Object: "chat.completion.chunk",
		Model:  f.originalModel,
		Choices: []OpenAIStreamDelta{{
			Index: matchedIndex,
		}},
	}
	stop := "stop"
	chunk.Choices[0].FinishReason = &stop
	return f.writeMarshaledChunk(chunk)
}

func (f *openAICompatibleStreamStopForwarder) chunkHasDeltaPayload(chunk OpenAIStreamChunk) bool {
	if len(chunk.Choices) == 0 {
		return chunk.Usage != nil
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil || choice.Delta.Role != nil || choice.Delta.FunctionCall != nil || len(choice.Delta.ToolCalls) > 0 || choice.Delta.Refusal != nil || choice.Delta.Audio != nil || choice.Delta.ContentNull || choice.FinishReason != nil {
			return true
		}
	}
	return chunk.Usage != nil
}

func (f *openAICompatibleStreamStopForwarder) writeData(data string) error {
	var payload strings.Builder
	writeSSEDataLines(&payload, data)
	payload.WriteByte('\n')
	return f.writePayload(payload.String())
}

func (f *openAICompatibleStreamStopForwarder) writePayload(payload string) error {
	if err := f.writer.WriteRaw(payload); err != nil {
		return err
	}
	f.wroteAny = true
	return nil
}
