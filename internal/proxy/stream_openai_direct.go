package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func forwardOpenAICompatibleSSEWithStops(ctx context.Context, w http.ResponseWriter, reader io.Reader, originalModel, requestName string, stops []string) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("ResponseWriter does not support flushing")
	}
	forwarder := &openAICompatibleStreamStopForwarder{
		ctx:             ctx,
		w:               w,
		flusher:         flusher,
		originalModel:   originalModel,
		requestName:     requestName,
		stops:           nonEmptyStopSequences(stops),
		stoppers:        make(map[int]*stopTextEnforcer),
		stoppedChoices:  make(map[int]bool),
		finishedChoices: make(map[int]bool),
	}
	if err := consumeSSERecords(reader, forwarder.handleRecord); err != nil {
		return err
	}
	return forwarder.finish()
}

type openAICompatibleStreamStopForwarder struct {
	ctx             context.Context
	w               http.ResponseWriter
	flusher         http.Flusher
	originalModel   string
	requestName     string
	stops           []string
	stoppers        map[int]*stopTextEnforcer
	stoppedChoices  map[int]bool
	finishedChoices map[int]bool
	terminated      bool
	sawDone         bool
}

func (f *openAICompatibleStreamStopForwarder) handleRecord(record sseRecord) error {
	data := record.data()
	if strings.TrimSpace(data) == OpenAIDoneMarker {
		f.sawDone = true
		return f.finish()
	}
	if f.terminated {
		return nil
	}
	if len(f.stops) == 0 {
		return f.writePayload(record.withData(f.rewriteModelOnly(data)))
	}

	warnUnknownFields(f.ctx, []byte(data), OpenAIStreamChunk{}, f.requestName+" stream chunk")
	chunk, parsed, err := f.parseChunk(data)
	if err != nil || !parsed {
		return f.writePayload(record.withData(f.rewriteModelOnly(data)))
	}

	matchedIndexes := make([]int, 0)
	for i := range chunk.Choices {
		index := chunk.Choices[i].Index
		if f.stoppedChoices[index] {
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
				f.stoppedChoices[index] = true
				f.finishedChoices[index] = true
				chunk.Choices[i].FinishReason = nil
			}
			if filtered == "" {
				chunk.Choices[i].Delta.Content = nil
			} else {
				chunk.Choices[i].Delta.Content = &filtered
			}
		}
		if chunk.Choices[i].FinishReason != nil {
			if err := f.writePendingStopTail(index); err != nil {
				return err
			}
			f.finishedChoices[index] = true
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
	if err := f.writePendingStopTails(); err != nil {
		return err
	}
	f.terminated = true
	if f.sawDone {
		return f.writeData(OpenAIDoneMarker)
	}
	return nil
}

func (f *openAICompatibleStreamStopForwarder) writePendingStopTail(index int) error {
	stopper := f.stoppers[index]
	if stopper == nil || stopper.Stopped() || f.stoppedChoices[index] || f.finishedChoices[index] {
		return nil
	}
	if tail := stopper.Flush(); tail != "" {
		chunk := OpenAIStreamChunk{Object: "chat.completion.chunk", Model: f.originalModel, Choices: []OpenAIStreamDelta{{Index: index, Delta: OpenAIDelta{Content: &tail}}}}
		return f.writeMarshaledChunk(chunk)
	}
	return nil
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
		stopper := f.stoppers[index]
		if stopper == nil || stopper.Stopped() || f.stoppedChoices[index] || f.finishedChoices[index] {
			continue
		}
		if tail := stopper.Flush(); tail != "" {
			chunk := OpenAIStreamChunk{
				Object: "chat.completion.chunk",
				Model:  f.originalModel,
				Choices: []OpenAIStreamDelta{{
					Index: index,
					Delta: OpenAIDelta{Content: &tail},
				}},
			}
			if err := f.writeMarshaledChunk(chunk); err != nil {
				return err
			}
		}
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

func (f *openAICompatibleStreamStopForwarder) parseChunk(data string) (OpenAIStreamChunk, bool, error) {
	var chunk OpenAIStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return OpenAIStreamChunk{}, false, err
	}
	chunk.Model = f.originalModel
	return chunk, true, nil
}

func (f *openAICompatibleStreamStopForwarder) rewriteModelOnly(data string) string {
	if strings.TrimSpace(data) == OpenAIDoneMarker {
		return data
	}
	warnUnknownFields(f.ctx, []byte(data), OpenAIStreamChunk{}, f.requestName+" stream chunk")
	var chunk map[string]interface{}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return data
	}
	chunk["model"] = f.originalModel
	updated, err := json.Marshal(chunk)
	if err != nil {
		return data
	}
	return string(updated)
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
			if choice.Delta.Content != nil {
				rawDelta["content"] = *choice.Delta.Content
			} else {
				delete(rawDelta, "content")
			}
			if choice.Delta.Role != nil {
				rawDelta["role"] = choice.Delta.Role
			}
			if len(choice.Delta.ToolCalls) > 0 {
				rawDelta["tool_calls"] = choice.Delta.ToolCalls
			}
			if choice.Delta.FunctionCall != nil {
				rawDelta["function_call"] = choice.Delta.FunctionCall
			}
			if choice.Delta.Refusal != nil {
				rawDelta["refusal"] = *choice.Delta.Refusal
			}
			if choice.Delta.Audio != nil {
				rawDelta["audio"] = choice.Delta.Audio
			}
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
	select {
	case <-f.ctx.Done():
		return f.ctx.Err()
	default:
	}
	logWireBytes(f.ctx, "WIRE CLIENT STREAM", []byte(payload))
	if _, err := io.WriteString(f.w, payload); err != nil {
		return err
	}
	f.flusher.Flush()
	return nil
}
