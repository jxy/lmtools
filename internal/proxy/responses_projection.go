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
	return s.conversationItemsForRecord(ctx, conv, false)
}

func (s *Server) conversationItemsForRecord(ctx context.Context, conv *conversationRecord, includeDeleted bool) ([]interface{}, error) {
	if conv == nil {
		return nil, fmt.Errorf("conversation is nil")
	}
	messages, err := session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, conv.SessionPath)
	if err != nil {
		return nil, err
	}
	return conversationItemsFromMessages(messages, 0, conversationDeletedItemSet(conv), includeDeleted), nil
}

func conversationItemsFromMessages(messages []core.TypedMessage, startIndex int, deleted map[string]bool, includeDeleted bool) []interface{} {
	items := coreResponsesInput(messages)
	out := make([]interface{}, 0, len(items))
	for i, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		cloned := cloneMapInterface(itemMap)
		if _, exists := itemMap["id"]; !exists {
			cloned["id"] = conversationItemID(startIndex + i)
		}
		if !includeDeleted && deleted[fmt.Sprint(cloned["id"])] {
			continue
		}
		out = append(out, cloned)
	}
	return out
}

func (s *Server) conversationHistory(ctx context.Context, conv *conversationRecord) ([]core.TypedMessage, error) {
	if conv == nil {
		return nil, fmt.Errorf("conversation is nil")
	}
	messages, err := session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, conv.SessionPath)
	if err != nil {
		return nil, err
	}
	deleted := conversationDeletedItemSet(conv)
	if len(deleted) == 0 {
		return messages, nil
	}
	return filterConversationHistoryDeletedItems(messages, deleted), nil
}

func filterConversationHistoryDeletedItems(messages []core.TypedMessage, deleted map[string]bool) []core.TypedMessage {
	if len(deleted) == 0 {
		return messages
	}
	projections := coreResponsesInputProjection(messages)
	visibleItemsByMessage := make(map[int]int)
	keptVisibleItemByMessage := make(map[int]bool)
	deletedBlocksByMessage := make(map[int]map[int]bool)
	for i, projected := range projections {
		if len(projected.BlockIndexes) == 0 {
			continue
		}
		visibleItemsByMessage[projected.MessageIndex]++
		if !deleted[projectedConversationItemID(projected.Item, i)] {
			keptVisibleItemByMessage[projected.MessageIndex] = true
			continue
		}
		blockSet := deletedBlocksByMessage[projected.MessageIndex]
		if blockSet == nil {
			blockSet = make(map[int]bool)
			deletedBlocksByMessage[projected.MessageIndex] = blockSet
		}
		for _, blockIndex := range projected.BlockIndexes {
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

func projectedConversationItemID(item interface{}, index int) string {
	if itemMap, ok := item.(map[string]interface{}); ok {
		if id, exists := itemMap["id"]; exists {
			return fmt.Sprint(id)
		}
	}
	return conversationItemID(index)
}

func conversationDeletedItemSet(conv *conversationRecord) map[string]bool {
	out := make(map[string]bool)
	if conv == nil {
		return out
	}
	for _, id := range conv.DeletedItemIDs {
		if id != "" {
			out[id] = true
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

func appendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func openAIResponsesCoreResponse(resp *OpenAIResponsesResponse) core.Response {
	text := resp.OutputText
	if text == "" {
		text = openAIResponsesOutputText(resp.Output)
	}
	return core.Response{
		Text:      text,
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
	for i, projected := range coreResponsesInputProjection(inputMessages) {
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
		if item.Type == "custom_tool_call" {
			calls = append(calls, core.ToolCall{
				ID:           firstNonEmpty(item.CallID, item.ID),
				Type:         "custom",
				Namespace:    item.Namespace,
				OriginalName: item.Name,
				Name:         responseOutputToolName(item),
				Args:         mustMarshalJSON(item.Input),
				Input:        item.Input,
			})
			continue
		}
		calls = append(calls, core.ToolCall{
			ID:           firstNonEmpty(item.CallID, item.ID),
			Type:         "function",
			Namespace:    item.Namespace,
			OriginalName: item.Name,
			Name:         responseOutputToolName(item),
			Args:         normalizeResponsesArguments(item.Arguments),
		})
	}
	return calls
}

func openAIResponsesOutputBlocks(output []OpenAIResponsesOutputItem) []core.Block {
	blocks := make([]core.Block, 0)
	for _, item := range output {
		switch item.Type {
		case "reasoning":
			raw := mustMarshalJSON(item)
			blocks = append(blocks, core.ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				ID:               item.ID,
				Status:           item.Status,
				Summary:          mustMarshalJSON(item.Summary),
				EncryptedContent: item.EncryptedContent,
				Raw:              raw,
			})
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					blocks = append(blocks, core.TextBlock{Text: part.Text})
				}
			}
		case "function_call":
			blocks = append(blocks, core.ToolUseBlock{
				ID:           firstNonEmpty(item.CallID, item.ID),
				Type:         "function",
				Namespace:    item.Namespace,
				OriginalName: item.Name,
				Name:         responseOutputToolName(item),
				Input:        normalizeResponsesArguments(item.Arguments),
			})
		case "custom_tool_call":
			blocks = append(blocks, core.ToolUseBlock{
				ID:           firstNonEmpty(item.CallID, item.ID),
				Type:         "custom",
				Namespace:    item.Namespace,
				OriginalName: item.Name,
				Name:         responseOutputToolName(item),
				Input:        mustMarshalJSON(item.Input),
				InputString:  item.Input,
			})
		}
	}
	return blocks
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
