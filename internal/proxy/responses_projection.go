package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/url"
	"strconv"
	"strings"
)

func responseRecordPayload(rec *responseRecord) interface{} {
	if rec == nil {
		return map[string]interface{}{"object": "response", "status": "failed"}
	}
	if len(rec.Raw) > 0 {
		var payload map[string]interface{}
		if err := json.Unmarshal(rec.Raw, &payload); err == nil {
			payload["id"] = rec.ID
			object := firstNonEmpty(rec.Object, "response")
			payload["object"] = object
			if object == "response" {
				payload["status"] = rec.Status
				if rec.Model != "" {
					payload["model"] = rec.Model
				}
			}
			if rec.Error != nil {
				payload["error"] = rec.Error
			}
			if rec.IncompleteDetails != nil {
				payload["incomplete_details"] = rec.IncompleteDetails
			}
			if rec.ConversationID != "" {
				payload["conversation"] = openAIResponsesConversationPayload(rec.ConversationID)
			}
			return payload
		}
	}
	return &OpenAIResponsesResponse{
		ID:                rec.ID,
		Object:            "response",
		CreatedAt:         rec.CreatedAt,
		Status:            rec.Status,
		Model:             rec.Model,
		Conversation:      openAIResponsesConversationRef(rec.ConversationID),
		Output:            []OpenAIResponsesOutputItem{},
		Error:             rec.Error,
		IncompleteDetails: rec.IncompleteDetails,
	}
}

func attachOpenAIResponsesConversation(resp *OpenAIResponsesResponse, id string) {
	if resp == nil {
		return
	}
	resp.Conversation = openAIResponsesConversationRef(id)
}

func openAIResponsesConversationRef(id string) *OpenAIResponsesConversation {
	if id == "" {
		return nil
	}
	return &OpenAIResponsesConversation{ID: id}
}

func openAIResponsesConversationPayload(id string) map[string]interface{} {
	return map[string]interface{}{"id": id}
}

func responseRecordInputItems(rec *responseRecord) []interface{} {
	if rec == nil || len(rec.Request) == 0 {
		return nil
	}
	var req OpenAIResponsesRequest
	if err := json.Unmarshal(rec.Request, &req); err != nil {
		return nil
	}
	return assignMissingItemIDs(responsesInputToItems(req.Input))
}

func responsesInputToItems(input interface{}) []interface{} {
	switch value := input.(type) {
	case nil:
		return nil
	case string:
		if value == "" {
			return nil
		}
		return []interface{}{map[string]interface{}{
			"type":    "message",
			"role":    string(core.RoleUser),
			"content": []map[string]interface{}{{"type": "input_text", "text": value}},
		}}
	case []interface{}:
		return append([]interface{}{}, value...)
	default:
		return []interface{}{value}
	}
}

func assignMissingItemIDs(items []interface{}) []interface{} {
	out := make([]interface{}, 0, len(items))
	for i, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		cloned := cloneMapInterface(itemMap)
		if _, exists := cloned["id"]; !exists {
			cloned["id"] = conversationItemID(i)
		}
		out = append(out, cloned)
	}
	return out
}

func conversationPayload(conv *conversationRecord) map[string]interface{} {
	return map[string]interface{}{
		"id":         conv.ID,
		"object":     "conversation",
		"created_at": conv.CreatedAt,
		"metadata":   cloneStringInterfaceMap(conv.Metadata),
	}
}

func listPayload(items []interface{}) map[string]interface{} {
	if items == nil {
		items = []interface{}{}
	}
	payload := map[string]interface{}{
		"object":   "list",
		"data":     items,
		"has_more": false,
	}
	if len(items) > 0 {
		if firstID := listItemID(items[0]); firstID != "" {
			payload["first_id"] = firstID
		}
		if lastID := listItemID(items[len(items)-1]); lastID != "" {
			payload["last_id"] = lastID
		}
	}
	return payload
}

type responseListPage struct {
	After string
	Limit int
	Order string
}

func parseResponseListPage(values url.Values) (responseListPage, error) {
	page := responseListPage{
		After: values.Get("after"),
		Limit: 20,
		Order: "desc",
	}
	if rawLimit := values.Get("limit"); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 100 {
			return page, fmt.Errorf("limit must be an integer between 1 and 100")
		}
		page.Limit = limit
	}
	if rawOrder := values.Get("order"); rawOrder != "" {
		switch rawOrder {
		case "asc", "desc":
			page.Order = rawOrder
		default:
			return page, fmt.Errorf("order must be 'asc' or 'desc'")
		}
	}
	return page, nil
}

func paginatedListPayload(items []interface{}, values url.Values) (map[string]interface{}, error) {
	page, err := parseResponseListPage(values)
	if err != nil {
		return nil, err
	}
	ordered := append([]interface{}{}, items...)
	if page.Order == "desc" {
		reverseInterfaceSlice(ordered)
	}
	if page.After != "" {
		index := -1
		for i, item := range ordered {
			if listItemID(item) == page.After {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("after cursor %q was not found", page.After)
		}
		ordered = ordered[index+1:]
	}
	hasMore := len(ordered) > page.Limit
	if hasMore {
		ordered = ordered[:page.Limit]
	}
	payload := listPayload(ordered)
	payload["has_more"] = hasMore
	return payload, nil
}

func reverseInterfaceSlice(items []interface{}) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func (s *Server) conversationItems(ctx context.Context, id string) ([]interface{}, error) {
	conv, ok, err := s.responsesState.loadConversation(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("conversation %q was not found", id)
	}
	items, _, err := s.conversationItemsForRecord(ctx, conv)
	return items, err
}

// conversationItemsForRecord lists the items of conversation conv, without
// the deleted ones, beside the stored item each one is.
func (s *Server) conversationItemsForRecord(_ context.Context, conv *conversationRecord) ([]interface{}, []storedItem, error) {
	if conv == nil {
		return nil, nil, fmt.Errorf("conversation is nil")
	}
	h, err := s.responsesState.store.History(session.Ref{Session: conv.Session, Message: conv.Head})
	if err != nil {
		return nil, nil, err
	}
	messages := h.Typed()
	projected, keys := lineageItems(messages, h)
	deleted := itemSet(conv.DeletedItems)
	items := make([]interface{}, 0, len(projected))
	var itemKeys []storedItem
	for i, p := range projected {
		if deleted[keys[i]] {
			continue
		}
		items = append(items, publicItem(p.Item, i))
		itemKeys = append(itemKeys, keys[i])
	}
	return items, itemKeys, nil
}

// conversationItemsFromMessages lists messages as conversation items, their
// IDs numbered from startIndex.
func conversationItemsFromMessages(messages []core.TypedMessage, startIndex int) []interface{} {
	items := core.OpenAIResponsesInput(messages)
	out := make([]interface{}, 0, len(items))
	for i, item := range items {
		out = append(out, publicItem(item, startIndex+i))
	}
	return out
}

// publicItem is item as the conversation API shows it: with its own ID, or
// one numbered by its place in the listing.
func publicItem(item interface{}, index int) interface{} {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return item
	}
	cloned := cloneMapInterface(itemMap)
	if _, exists := itemMap["id"]; !exists {
		cloned["id"] = conversationItemID(index)
	}
	return cloned
}

// storedItem names a conversation item by the stored message it comes from
// and its place among that message's items, which holds on every branch
// that contains the message.
type storedItem struct {
	Message string `json:"message"`
	Item    int    `json:"item"`
}

func itemSet(items []storedItem) map[storedItem]bool {
	set := make(map[storedItem]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func appendUniqueItem(items []storedItem, item storedItem) []storedItem {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

// lineageItems projects messages, the typed form of history h, to their
// Responses input items, each beside the stored item it is.
func lineageItems(messages []core.TypedMessage, h session.History) ([]core.OpenAIResponsesInputProjectionItem, []storedItem) {
	projected := core.OpenAIResponsesInputProjection(messages)
	keys := make([]storedItem, len(projected))
	counts := make(map[int]int)
	for i, p := range projected {
		keys[i] = storedItem{Message: h.Messages[p.MessageIndex].ID, Item: counts[p.MessageIndex]}
		counts[p.MessageIndex]++
	}
	return projected, keys
}

// withoutItems returns the typed history h without the stored items in
// hidden: a message loses the blocks of its hidden items, and a message
// whose visible items are all hidden is left out.
func withoutItems(h session.History, hidden []storedItem) []core.TypedMessage {
	messages := h.Typed()
	if len(hidden) == 0 {
		return messages
	}
	deleted := itemSet(hidden)
	projected, keys := lineageItems(messages, h)
	visibleItemsByMessage := make(map[int]int)
	keptVisibleItemByMessage := make(map[int]bool)
	deletedBlocksByMessage := make(map[int]map[int]bool)
	for i, p := range projected {
		if len(p.BlockIndexes) == 0 {
			continue
		}
		visibleItemsByMessage[p.MessageIndex]++
		if !deleted[keys[i]] {
			keptVisibleItemByMessage[p.MessageIndex] = true
			continue
		}
		blockSet := deletedBlocksByMessage[p.MessageIndex]
		if blockSet == nil {
			blockSet = make(map[int]bool)
			deletedBlocksByMessage[p.MessageIndex] = blockSet
		}
		for _, blockIndex := range p.BlockIndexes {
			blockSet[blockIndex] = true
		}
	}

	out := make([]core.TypedMessage, 0, len(messages))
	for i, msg := range messages {
		if visibleItemsByMessage[i] > 0 && !keptVisibleItemByMessage[i] {
			continue
		}
		deletedBlocks := deletedBlocksByMessage[i]
		if len(deletedBlocks) == 0 {
			out = append(out, msg)
			continue
		}
		filtered := core.TypedMessage{
			Role:   msg.Role,
			Blocks: make([]core.Block, 0, len(msg.Blocks)),
		}
		for blockIndex, block := range msg.Blocks {
			if deletedBlocks[blockIndex] {
				continue
			}
			filtered.Blocks = append(filtered.Blocks, block)
		}
		if len(filtered.Blocks) > 0 {
			out = append(out, filtered)
		}
	}
	return out
}

func conversationItemID(index int) string {
	return fmt.Sprintf("item_%04d", index)
}

func listItemID(item interface{}) string {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := itemMap["id"].(string)
	return id
}

func openAIResponsesCoreResponse(resp *OpenAIResponsesResponse) core.Response {
	return core.Response{
		Text:      openAIResponsesOutputText(resp.Output),
		ToolCalls: openAIResponsesOutputToolCalls(resp.Output),
		Blocks:    openAIResponsesOutputBlocks(resp.Output),
	}
}

func compactedResponseOutputItems(stateCtx *openAIResponsesStateContext, inputMessages []core.TypedMessage, summary string) []OpenAIResponsesOutputItem {
	output := make([]OpenAIResponsesOutputItem, 0, len(inputMessages)+1)
	historyLen := 0
	if stateCtx != nil {
		historyLen = len(stateCtx.History)
	}
	for i, projected := range core.OpenAIResponsesInputProjection(inputMessages) {
		itemMap, ok := projected.Item.(map[string]interface{})
		if !ok || itemMap["type"] != "message" {
			continue
		}
		role, _ := itemMap["role"].(string)
		if projected.MessageIndex < historyLen && role == string(core.RoleAssistant) {
			continue
		}
		content := responsesOutputContentParts(itemMap["content"])
		output = append(output, OpenAIResponsesOutputItem{
			ID:      conversationItemID(i),
			Type:    "message",
			Status:  "completed",
			Role:    core.Role(role),
			Content: content,
		})
	}
	output = append(output, OpenAIResponsesOutputItem{
		ID:               generateUUID("cmp_"),
		Type:             "compaction",
		EncryptedContent: summary,
	})
	return output
}

func responsesOutputContentParts(raw interface{}) []OpenAIResponsesContentPart {
	rawParts, ok := raw.([]map[string]interface{})
	if !ok {
		return nil
	}
	parts := make([]OpenAIResponsesContentPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		partType, _ := rawPart["type"].(string)
		text, _ := rawPart["text"].(string)
		if text == "" {
			continue
		}
		parts = append(parts, OpenAIResponsesContentPart{Type: partType, Text: text})
	}
	return parts
}

func anthropicResponseText(resp *AnthropicResponse) string {
	if resp == nil {
		return ""
	}
	parts := make([]string, 0, len(resp.Content))
	for _, block := range resp.Content {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

func anthropicUsageToResponsesUsage(resp *AnthropicResponse) *OpenAIResponsesUsage {
	if resp == nil || resp.Usage == nil {
		return nil
	}
	inputTokens := resp.Usage.InputTokens
	outputTokens := resp.Usage.OutputTokens
	return &OpenAIResponsesUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		InputTokensDetails: &OpenAIResponsesInputDetails{
			CachedTokens: resp.Usage.CacheReadInputTokens,
		},
	}
}

func openAIResponsesOutputText(output []OpenAIResponsesOutputItem) string {
	parts := make([]string, 0)
	for _, item := range output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
	}
	return strings.Join(parts, "")
}

func openAIResponsesOutputToolCalls(output []OpenAIResponsesOutputItem) []core.ToolCall {
	calls := make([]core.ToolCall, 0)
	for _, item := range output {
		if item.Type != "function_call" && item.Type != "custom_tool_call" {
			continue
		}
		// Reuse the shared tool-use block conversion so function/custom
		// classification, name flattening, and argument normalization stay in
		// one place. core.ToolCall.Args/Input mirror ToolUseBlock.Input/InputString.
		block := openAIResponsesOutputToolUseBlock(item)
		calls = append(calls, core.ToolCall{
			ID:           block.ID,
			Type:         block.Type,
			Namespace:    block.Namespace,
			OriginalName: block.OriginalName,
			Name:         block.Name,
			Args:         block.Input,
			Input:        block.InputString,
		})
	}
	return calls
}

func openAIResponsesOutputBlocks(output []OpenAIResponsesOutputItem) []core.Block {
	blocks := make([]core.Block, 0)
	for _, item := range output {
		blocks = append(blocks, openAIResponsesOutputItemBlocks(item)...)
	}
	return blocks
}

func openAIResponsesOutputItemBlocks(item OpenAIResponsesOutputItem) []core.Block {
	switch item.Type {
	case "reasoning":
		return []core.Block{core.ReasoningBlock{
			Provider:         "openai",
			Type:             "reasoning",
			ID:               item.ID,
			Status:           item.Status,
			Summary:          mustMarshalJSON(item.Summary),
			EncryptedContent: item.EncryptedContent,
			Raw:              mustMarshalJSON(item),
		}}
	case "message":
		blocks := make([]core.Block, 0, len(item.Content))
		for _, part := range item.Content {
			if part.Text != "" {
				blocks = append(blocks, core.TextBlock{Text: part.Text})
			}
		}
		return blocks
	case "function_call", "custom_tool_call":
		return []core.Block{openAIResponsesOutputToolUseBlock(item)}
	default:
		return nil
	}
}

func openAIResponsesOutputToolUseBlock(item OpenAIResponsesOutputItem) core.ToolUseBlock {
	block := core.ToolUseBlock{
		ID:           firstNonEmpty(item.CallID, item.ID),
		Namespace:    item.Namespace,
		OriginalName: item.Name,
		Name:         responseOutputToolName(item),
	}
	if item.Type == "custom_tool_call" {
		block.Type = "custom"
		block.Input = mustMarshalJSON(item.Input)
		block.InputString = item.Input
		return block
	}
	block.Type = "function"
	block.Input = core.NormalizeOpenAIResponsesArguments(item.Arguments)
	return block
}

func mustMarshalJSON(value interface{}) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func marshalJSONRaw(value interface{}) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	return data, nil
}

func cloneMapInterface(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
