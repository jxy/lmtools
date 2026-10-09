package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type openAIResponsesCompletion struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (c openAIResponsesCompletion) incompleteReason() string {
	if c.IncompleteDetails != nil && c.IncompleteDetails.Reason != "" {
		return c.IncompleteDetails.Reason
	}
	if c.Status != "" && c.Status != "completed" {
		return c.Status
	}
	return ""
}

type responsesStreamEvent struct {
	Type         string          `json:"type"`
	Delta        string          `json:"delta"`
	Text         string          `json:"text"`
	ItemID       string          `json:"item_id"`
	OutputIndex  *int            `json:"output_index"`
	ContentIndex int             `json:"content_index"`
	SummaryIndex int             `json:"summary_index"`
	Item         json.RawMessage `json:"item"`
	Part         json.RawMessage `json:"part"`
	Response     struct {
		openAIResponsesCompletion
		Output []json.RawMessage `json:"output"`
		Error  json.RawMessage   `json:"error"`
	} `json:"response"`
	Error json.RawMessage `json:"error"`
}

type responsesStreamPart struct {
	kind     string
	partType string
	index    int
	text     strings.Builder
}

type responsesStreamItem struct {
	id             string
	index          *int
	fields         map[string]json.RawMessage
	arguments      strings.Builder
	argumentDeltas bool
	complete       bool
	fullArguments  bool
	reasoningParts []*responsesStreamPart
	reasoningEnded bool
	textParts      []*responsesStreamPart
	fullContent    bool
	fullSummary    bool
}

// OpenAIResponsesStreamState tracks output items by their explicit index or
// item ID. An omitted output_index must never become the real index zero.
type OpenAIResponsesStreamState struct {
	items            []*responsesStreamItem
	itemsByID        map[string]*responsesStreamItem
	itemsByIndex     map[int]*responsesStreamItem
	streamEvents     []ResponseStreamEvent
	reasoningOpen    bool
	text             strings.Builder
	unidentifiedText *responsesStreamItem
	usage            *Usage
	terminal         bool
	snapshotOrder    bool
	incompleteReason string
}

func NewOpenAIResponsesStreamState() *OpenAIResponsesStreamState {
	return &OpenAIResponsesStreamState{
		itemsByID:    make(map[string]*responsesStreamItem),
		itemsByIndex: make(map[int]*responsesStreamItem),
	}
}

func (s *OpenAIResponsesStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	text, calls, done, err := s.parseLine(line)
	if err != nil {
		// Parsing can update an item before a later field in the event fails.
		// Stop the state as well as the stream: a later marker must never
		// authorize calls from a rejected event or terminal snapshot.
		s.terminal = true
		if s.incompleteReason == "" {
			s.incompleteReason = "stream_interrupted"
		}
		s.finishReasoning(true)
		return "", nil, false, newFatalStreamError(err)
	}
	return text, calls, done, nil
}

func (s *OpenAIResponsesStreamState) parseLine(line string) (string, []ToolCall, bool, error) {
	data, ok := streamFieldValue(line, "data")
	if !ok || s.terminal {
		return "", nil, s.terminal, nil
	}
	if data == "[DONE]" {
		s.terminal = true
		for _, item := range s.items {
			item.reasoningEnded = true
		}
		text, err := s.remainingText()
		if err != nil {
			return "", nil, false, err
		}
		calls, err := s.completedToolCalls()
		s.finishReasoning(s.incompleteReason != "")
		return text, calls, true, err
	}

	var event responsesStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", nil, false, err
	}
	if hasResponsesJSON(event.Error) {
		return "", nil, false, newFatalStreamError(fmt.Errorf("upstream stream error: %s", formatOpenAIStreamError(event.Error)))
	}

	switch event.Type {
	case "error":
		return "", nil, false, newFatalStreamError(fmt.Errorf("OpenAI responses stream error"))
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta == "" {
			return "", nil, false, nil
		}
		item, err := s.resolveTextItem(event.OutputIndex, event.ItemID)
		if err != nil {
			return "", nil, false, err
		}
		partType := "output_text"
		if event.Type == "response.refusal.delta" {
			partType = "refusal"
		}
		part := item.textPart(event.ContentIndex, partType)
		part.text.WriteString(event.Delta)
		s.finishReasoning(false)
		s.text.WriteString(event.Delta)
		return event.Delta, nil, false, nil
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		item, err := s.resolveItem(event.OutputIndex, event.ItemID)
		if err != nil {
			return "", nil, false, err
		}
		if !item.complete {
			item.argumentDeltas = true
			item.arguments.WriteString(event.Delta)
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_text.done", "response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		item, err := s.resolveItem(event.OutputIndex, event.ItemID)
		if err != nil {
			return "", nil, false, err
		}
		kind, index := "content", event.ContentIndex
		if strings.Contains(event.Type, "summary") {
			kind, index = "summary", event.SummaryIndex
		}
		if strings.HasSuffix(event.Type, ".delta") {
			s.reasoningDelta(item, kind, index, event.Delta)
		} else {
			text := event.Text
			if hasResponsesJSON(event.Part) {
				var part struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(event.Part, &part); err != nil {
					return "", nil, false, err
				}
				text = part.Text
			}
			s.reasoningSnapshot(item, kind, index, text)
		}
	case "response.output_item.added", "response.output_item.done":
		item, err := s.captureOutputItem(event.OutputIndex, event.ItemID, event.Item, event.Type == "response.output_item.done")
		if err != nil {
			return "", nil, false, err
		}
		if item == nil {
			return "", nil, false, nil
		}
		if event.Type == "response.output_item.done" {
			item.reasoningEnded = true
		}
		if item.fieldString("type") == "message" {
			text, err := s.remainingText()
			if text != "" {
				s.finishReasoning(false)
			}
			return text, nil, false, err
		}
	case "response.completed", "response.incomplete":
		s.usage = s.usage.MergedWith(UsageFromPayload([]byte(data)))
		s.incompleteReason = event.Response.incompleteReason()
		if event.Type == "response.incomplete" && s.incompleteReason == "" {
			s.incompleteReason = "incomplete"
		}
		if err := s.reconcileOutput(event.Response.Output); err != nil {
			return "", nil, false, err
		}
		text, err := s.remainingText()
		if err != nil {
			return "", nil, false, err
		}
		s.terminal = true
		calls, err := s.completedToolCalls()
		s.finishReasoning(s.incompleteReason != "")
		return text, calls, true, err
	case "response.failed":
		if hasResponsesJSON(event.Response.Error) {
			return "", nil, false, newFatalStreamError(fmt.Errorf("API error: %s", formatOpenAIStreamError(event.Response.Error)))
		}
		return "", nil, false, newFatalStreamError(fmt.Errorf("OpenAI responses stream failed"))
	}
	return "", nil, false, nil
}

func hasResponsesJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func (s *OpenAIResponsesStreamState) resolveItem(index *int, id string) (*responsesStreamItem, error) {
	var item *responsesStreamItem
	if id != "" {
		item = s.itemsByID[id]
	}
	if index != nil {
		if item != nil && item.index != nil && *item.index != *index {
			return nil, newFatalStreamError(fmt.Errorf("responses output item %q changed its index from %d to %d", id, *item.index, *index))
		}
		indexed := s.itemsByIndex[*index]
		if indexed != nil {
			if (id != "" && indexed.id != "" && indexed.id != id) || (item != nil && item != indexed) {
				return nil, newFatalStreamError(fmt.Errorf("conflicting Responses output item identity at index %d", *index))
			}
			item = indexed
		}
	}
	if item == nil {
		if index == nil && id == "" {
			return nil, newFatalStreamError(fmt.Errorf("responses output item is missing both output_index and item ID"))
		}
		item = &responsesStreamItem{fields: make(map[string]json.RawMessage)}
		s.items = append(s.items, item)
	}
	if id != "" {
		item.id = id
		s.itemsByID[id] = item
	}
	if index != nil {
		value := *index
		item.index = &value
		s.itemsByIndex[value] = item
	}
	return item, nil
}

func (s *OpenAIResponsesStreamState) resolveTextItem(index *int, id string) (*responsesStreamItem, error) {
	if index != nil || id != "" {
		item, err := s.resolveItem(index, id)
		if err != nil {
			return nil, err
		}
		if kind := item.fieldString("type"); kind != "" && kind != "message" {
			return nil, newFatalStreamError(fmt.Errorf("responses text delta identifies a %s item", kind))
		}
		item.fields["type"], _ = json.Marshal("message")
		return item, nil
	}
	if s.unidentifiedText != nil {
		return s.unidentifiedText, nil
	}
	var candidate *responsesStreamItem
	for _, item := range s.items {
		if item.fieldString("type") == "message" && !item.complete {
			if candidate != nil {
				candidate = nil
				break
			}
			candidate = item
		}
	}
	if candidate != nil {
		return candidate, nil
	}
	// Retain unidentifiable text as its own message. A supplied terminal
	// output array replaces it with the provider's canonical message items.
	item := &responsesStreamItem{fields: make(map[string]json.RawMessage)}
	item.fields["type"], _ = json.Marshal("message")
	s.items = append(s.items, item)
	s.unidentifiedText = item
	return item, nil
}

func (item *responsesStreamItem) textPart(index int, partType string) *responsesStreamPart {
	for _, part := range item.textParts {
		if part.index == index {
			return part
		}
	}
	part := &responsesStreamPart{kind: "content", partType: partType, index: index}
	// A backend may put an initial prefix in output_item.added before it
	// sends deltas. Preserve that prefix when extending this content part.
	var content []struct {
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	}
	if json.Unmarshal(item.fields["content"], &content) == nil && index >= 0 && index < len(content) {
		text := content[index].Text
		if partType == "refusal" {
			text = content[index].Refusal
		}
		part.text.WriteString(text)
	}
	item.textParts = append(item.textParts, part)
	return part
}

func (s *OpenAIResponsesStreamState) captureOutputItem(index *int, id string, raw json.RawMessage, complete bool) (*responsesStreamItem, error) {
	if !hasResponsesJSON(raw) {
		return nil, nil
	}
	var parsed openAIResponsesOutputItem
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if rawID := fields["id"]; len(rawID) > 0 {
		var itemID string
		if err := json.Unmarshal(rawID, &itemID); err != nil {
			return nil, err
		}
		if id != "" && itemID != "" && id != itemID {
			return nil, newFatalStreamError(fmt.Errorf("conflicting Responses item IDs %q and %q", id, itemID))
		}
		if itemID != "" {
			id = itemID
		}
	}
	item, err := s.resolveItem(index, id)
	if err != nil {
		return nil, err
	}
	for key, value := range fields {
		item.fields[key] = value
	}
	if complete {
		if _, supplied := fields["content"]; supplied {
			item.fullContent = true
		}
		if _, supplied := fields["summary"]; supplied {
			item.fullSummary = true
		}
		var status string
		_ = json.Unmarshal(fields["status"], &status)
		item.complete = status == "" || status == "completed"
		if _, supplied := fields["status"]; !supplied {
			delete(item.fields, "status")
		}
		key := "arguments"
		if item.fieldString("type") == "custom_tool_call" {
			key = "input"
		}
		_, item.fullArguments = fields[key]
	}
	if item.fieldString("type") == "reasoning" {
		for _, kind := range []string{"summary", "content"} {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(fields[kind], &parts) == nil {
				for i, part := range parts {
					if part.Type == "summary_text" || part.Type == "reasoning_text" || part.Type == "text" {
						s.reasoningSnapshot(item, kind, i, part.Text)
					}
				}
			}
		}
	}
	return item, nil
}

// The terminal output array is authoritative for ordering and full payloads.
// Match its IDs before considering positions: an earlier event may omit indices.
func (s *OpenAIResponsesStreamState) reconcileOutput(output []json.RawMessage) error {
	var ordered []*responsesStreamItem
	seen := make(map[*responsesStreamItem]bool, len(output))
	for index, raw := range output {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			return err
		}
		var itemIndex *int
		if identity.ID == "" || s.itemsByID[identity.ID] == nil {
			// Only bind a snapshot position to an existing indexed item when
			// that item's ID agrees or was never supplied.
			indexed := s.itemsByIndex[index]
			if indexed == nil || indexed.id == "" || indexed.id == identity.ID {
				itemIndex = &index
			}
		}
		item, err := s.captureOutputItem(itemIndex, identity.ID, raw, true)
		if err != nil {
			return err
		}
		if item == nil || seen[item] {
			continue
		}
		ordered = append(ordered, item)
		seen[item] = true
	}
	if output != nil {
		s.items, s.snapshotOrder = ordered, true
	}
	return nil
}

func (s *OpenAIResponsesStreamState) completedToolCalls() ([]ToolCall, error) {
	if s.incompleteReason != "" {
		return nil, nil
	}
	for _, item := range s.items {
		// Match buffered Responses handling: every explicit unfinished item
		// makes the response incomplete, including messages and reasoning.
		if status := item.fieldString("status"); status != "" && status != "completed" {
			s.incompleteReason = "incomplete"
			return nil, nil
		}
		if item.isTool() && !item.complete {
			s.incompleteReason = "stream_interrupted"
			return nil, nil
		}
	}
	var calls []ToolCall
	for _, item := range s.orderedItems() {
		call, err := item.completedToolCall()
		if err != nil {
			return nil, err
		}
		calls = append(calls, call...)
	}
	return calls, nil
}

func (item *responsesStreamItem) fieldString(key string) string {
	var value string
	_ = json.Unmarshal(item.fields[key], &value)
	return value
}

func (item *responsesStreamItem) isTool() bool {
	kind := item.fieldString("type")
	return kind == "function_call" || kind == "custom_tool_call"
}

func (item *responsesStreamItem) completedToolCall() ([]ToolCall, error) {
	if !item.isTool() || !item.complete {
		return nil, nil
	}
	id := item.fieldString("call_id")
	if id == "" {
		id = item.id
	}
	name := item.fieldString("name")
	if id == "" || name == "" {
		return nil, newFatalStreamError(fmt.Errorf("completed Responses tool call is missing its ID or name"))
	}
	call := ToolCall{ID: id, Name: name}
	key := "arguments"
	if item.fieldString("type") == "custom_tool_call" {
		call.Type, key = "custom", "input"
	}
	arguments := item.fieldString(key)
	if !item.fullArguments && item.argumentDeltas {
		arguments = item.arguments.String()
		item.fields[key], _ = json.Marshal(arguments)
	}
	if call.Type == "custom" {
		call.Input, call.Args = arguments, jsonStringRawMessage(arguments)
	} else {
		// Completion is required even when a fragment happens to be valid
		// JSON. Conversely, final full arguments override partial deltas.
		if strings.TrimSpace(arguments) != "" && !json.Valid([]byte(arguments)) {
			return nil, newFatalStreamError(fmt.Errorf("invalid JSON in completed Responses tool call %q arguments", id))
		}
		call.Args = NormalizeOpenAIResponsesArguments(arguments)
	}
	return []ToolCall{call}, nil
}

func (s *OpenAIResponsesStreamState) reasoningPart(item *responsesStreamItem, kind string, index int) *responsesStreamPart {
	for i := range item.reasoningParts {
		part := item.reasoningParts[i]
		if part.kind == kind && part.index == index {
			return part
		}
	}
	item.reasoningParts = append(item.reasoningParts, &responsesStreamPart{kind: kind, index: index})
	return item.reasoningParts[len(item.reasoningParts)-1]
}

func (s *OpenAIResponsesStreamState) reasoningDelta(item *responsesStreamItem, kind string, index int, text string) {
	if text == "" || item.reasoningEnded {
		return
	}
	if !s.reasoningOpen {
		s.reasoningOpen = true
		s.streamEvents = append(s.streamEvents, ResponseStreamEvent{Type: ResponseStreamReasoningStart, ReasoningType: "reasoning"})
	}
	part := s.reasoningPart(item, kind, index)
	if part.text.Len() == 0 {
		for i := range item.reasoningParts {
			if item.reasoningParts[i].text.Len() > 0 {
				s.streamEvents = append(s.streamEvents, ResponseStreamEvent{Type: ResponseStreamReasoningDelta, ReasoningType: "reasoning", Text: "\n"})
				break
			}
		}
	}
	part.text.WriteString(text)
	s.streamEvents = append(s.streamEvents, ResponseStreamEvent{Type: ResponseStreamReasoningDelta, ReasoningType: "reasoning", Text: text})
}

func (s *OpenAIResponsesStreamState) reasoningSnapshot(item *responsesStreamItem, kind string, index int, text string) {
	part := s.reasoningPart(item, kind, index)
	if strings.HasPrefix(text, part.text.String()) {
		s.reasoningDelta(item, kind, index, strings.TrimPrefix(text, part.text.String()))
	}
}

// All readable reasoning shares one presentation section. Output items can
// interleave, while the presenter deliberately has no provider item IDs.
func (s *OpenAIResponsesStreamState) finishReasoning(incomplete bool) {
	if s.reasoningOpen {
		s.reasoningOpen = false
		s.streamEvents = append(s.streamEvents, ResponseStreamEvent{
			Type: ResponseStreamReasoningEnd, ReasoningType: "reasoning", Incomplete: incomplete,
		})
	}
}

func (item *responsesStreamItem) raw() json.RawMessage {
	fields := make(map[string]json.RawMessage, len(item.fields)+3)
	for key, value := range item.fields {
		fields[key] = value
	}
	if len(item.reasoningParts) > 0 {
		fields["type"], _ = json.Marshal("reasoning")
		if item.id != "" {
			fields["id"], _ = json.Marshal(item.id)
		}
		if !item.fullContent {
			if content := mergeResponsesStreamParts(fields["content"], item.reasoningParts, "content"); len(content) > 0 {
				fields["content"] = content
			}
		}
		if !item.fullSummary {
			if summary := mergeResponsesStreamParts(fields["summary"], item.reasoningParts, "summary"); len(summary) > 0 {
				fields["summary"] = summary
			}
		}
	}
	if len(item.textParts) > 0 && !item.fullContent {
		fields["type"], _ = json.Marshal("message")
		fields["content"] = mergeResponsesStreamParts(fields["content"], item.textParts, "content")
	}
	raw, _ := json.Marshal(fields)
	return raw
}

// Incomplete items keep the full readable prefix observed so far. Once a
// completed item or terminal snapshot supplies this field, its content wins.
func mergeResponsesStreamParts(original json.RawMessage, parts []*responsesStreamPart, kind string) json.RawMessage {
	var content []json.RawMessage
	_ = json.Unmarshal(original, &content)
	originalCount := len(content)
	ordered := append([]*responsesStreamPart(nil), parts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
	for _, part := range ordered {
		if part.kind != kind || part.text.Len() == 0 {
			continue
		}
		fields := make(map[string]json.RawMessage)
		if part.index >= 0 && part.index < originalCount {
			_ = json.Unmarshal(content[part.index], &fields)
		}
		if fields == nil {
			fields = make(map[string]json.RawMessage)
		}
		partType := part.partType
		if partType == "" {
			partType = "reasoning_text"
			if kind == "summary" {
				partType = "summary_text"
			}
		}
		fields["type"], _ = json.Marshal(partType)
		textKey := "text"
		if partType == "refusal" {
			textKey = "refusal"
		}
		fields[textKey], _ = json.Marshal(part.text.String())
		raw, _ := json.Marshal(fields)
		if part.index >= 0 && part.index < originalCount {
			content[part.index] = raw
		} else {
			content = append(content, raw)
		}
	}
	if len(content) == 0 {
		return original
	}
	raw, _ := json.Marshal(content)
	return raw
}

func (s *OpenAIResponsesStreamState) orderedItems() []*responsesStreamItem {
	items := append([]*responsesStreamItem(nil), s.items...)
	// When all events use indices, output order follows those indices even
	// if completion events arrive out of order. ID-only events keep arrival
	// order until the terminal snapshot supplies the final order.
	allIndexed := !s.snapshotOrder
	for _, item := range items {
		allIndexed = allIndexed && item.index != nil
	}
	if allIndexed {
		sort.SliceStable(items, func(i, j int) bool { return *items[i].index < *items[j].index })
	}
	return items
}

func (s *OpenAIResponsesStreamState) Blocks() []Block {
	var blocks []Block
	for _, item := range s.orderedItems() {
		blocks = append(blocks, openAIResponsesOutputItemBlocks(item.raw())...)
	}
	return blocks
}

func (s *OpenAIResponsesStreamState) remainingText() (string, error) {
	var full strings.Builder
	for _, block := range s.Blocks() {
		if text, ok := block.(TextBlock); ok {
			full.WriteString(text.Text)
		}
	}
	if strings.HasPrefix(full.String(), s.text.String()) {
		remainder := strings.TrimPrefix(full.String(), s.text.String())
		s.text.WriteString(remainder)
		return remainder, nil
	}
	fullMessage := s.snapshotOrder
	for _, item := range s.items {
		fullMessage = fullMessage || (item.fieldString("type") == "message" && item.fullContent)
	}
	if fullMessage {
		return "", newFatalStreamError(fmt.Errorf("responses output text conflicts with streamed answer"))
	}
	return "", nil
}

func (s *OpenAIResponsesStreamState) drainResponseStreamEvents() []ResponseStreamEvent {
	events := s.streamEvents
	s.streamEvents = nil
	return events
}

func (s *OpenAIResponsesStreamState) Usage() *Usage { return s.usage }

// IncompleteReason also catches EOF without a terminal event, including a
// transport interruption while a tool call or reasoning block was unfinished.
func (s *OpenAIResponsesStreamState) IncompleteReason() string {
	if !s.terminal {
		return "stream_interrupted"
	}
	return s.incompleteReason
}
