package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewMinimalTestServerDefaultsResponsesStateToTempDir(t *testing.T) {
	config := &Config{}
	server := NewMinimalTestServer(t, config)
	if strings.TrimSpace(config.SessionsDir) == "" {
		t.Fatal("SessionsDir was not defaulted for test server")
	}
	if server.responsesState.root != config.SessionsDir {
		t.Fatalf("responses state root = %q, want %q", server.responsesState.root, config.SessionsDir)
	}
	if strings.Contains(server.responsesState.root, ".apiproxy") {
		t.Fatalf("responses state root uses production path: %q", server.responsesState.root)
	}
}

func TestOpenAIResponsesReasoningInputRoundTrip(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5",
		Input: []interface{}{
			map[string]interface{}{
				"type":              "reasoning",
				"id":                "rs_123",
				"status":            "completed",
				"summary":           []interface{}{map[string]interface{}{"type": "summary_text", "text": "checked"}},
				"encrypted_content": "enc_abc",
			},
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup",
				"arguments": `{"q":"x"}`,
			},
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_123",
				"output":  "result",
			},
		},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	rendered, err := TypedToOpenAIResponsesRequest(typed, "gpt-5")
	if err != nil {
		t.Fatalf("TypedToOpenAIResponsesRequest() error = %v", err)
	}
	input, ok := rendered.Input.([]interface{})
	if !ok {
		t.Fatalf("rendered input type = %T, want []interface{}", rendered.Input)
	}
	if len(input) != 3 {
		t.Fatalf("rendered input length = %d, want 3: %#v", len(input), input)
	}
	reasoning, ok := input[0].(map[string]interface{})
	if !ok {
		t.Fatalf("reasoning item type = %T", input[0])
	}
	if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_123" || reasoning["encrypted_content"] != "enc_abc" {
		t.Fatalf("reasoning item not preserved: %#v", reasoning)
	}
	if call, _ := input[1].(map[string]interface{}); call["type"] != "function_call" {
		t.Fatalf("second item = %#v, want function_call", input[1])
	}
	if output, _ := input[2].(map[string]interface{}); output["type"] != "function_call_output" {
		t.Fatalf("third item = %#v, want function_call_output", input[2])
	}
}

func TestResponsesStateCancelResponseIfPendingDoesNotOverwriteCompleted(t *testing.T) {
	state := newResponsesState(t.TempDir())
	respID := "resp_race_complete"
	if err := state.saveResponse(&responseRecord{
		Version:   responsesStateVersion,
		ID:        respID,
		Object:    "response",
		Status:    "completed",
		Model:     "gpt-test",
		CreatedAt: time.Now().Unix(),
		Raw:       mustMarshalJSON(&OpenAIResponsesResponse{ID: respID, Object: "response", Status: "completed", Model: "gpt-test"}),
	}); err != nil {
		t.Fatalf("saveResponse(completed) error = %v", err)
	}

	rec, ok, err := state.cancelResponseIfPending(respID, map[string]interface{}{"code": "cancelled"})
	if err != nil || !ok {
		t.Fatalf("cancelResponseIfPending() = rec:%#v ok:%v err:%v", rec, ok, err)
	}
	if rec.Status != "completed" {
		t.Fatalf("cancelled completed record: status = %q", rec.Status)
	}
	stored, ok, err := state.loadResponse(respID)
	if err != nil || !ok || stored.Status != "completed" {
		t.Fatalf("stored response = rec:%#v ok:%v err:%v, want completed", stored, ok, err)
	}
}

func TestOpenAIResponsesBackgroundCommitRequiresActiveRecordBeforeSessionAppend(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientForTesting(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger)
	server := NewTestServerDirectWithClient(t, config, client)
	sess, err := server.responsesState.createSession()
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	respID := "resp_cancelled_before_commit"
	if err := server.responsesState.saveResponse(&responseRecord{
		Version:     responsesStateVersion,
		ID:          respID,
		Object:      "response",
		Status:      "cancelled",
		Model:       "claude-test",
		SessionPath: sess.Path,
		CreatedAt:   time.Now().Unix(),
		Background:  true,
		Store:       true,
	}); err != nil {
		t.Fatalf("saveResponse(cancelled) error = %v", err)
	}

	err = server.commitOpenAIResponsesStateWithBlocks(
		context.Background(),
		&openAIResponsesStateContext{
			Session:          sess,
			Store:            true,
			Background:       true,
			ExistingRecordID: respID,
		},
		&OpenAIResponsesRequest{Model: "claude-test", Input: "hello"},
		TypedRequest{Messages: []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), "hello")}},
		&OpenAIResponsesResponse{
			ID:        respID,
			Object:    "response",
			Status:    "completed",
			Model:     "claude-test",
			CreatedAt: time.Now().Unix(),
			Output: []OpenAIResponsesOutputItem{{
				Type:   "message",
				Status: "completed",
				Role:   core.RoleAssistant,
				Content: []OpenAIResponsesContentPart{{
					Type: "output_text",
					Text: "finished",
				}},
			}},
		},
		"claude-test",
		nil,
	)
	if err == nil || !stdErrors.Is(err, errResponsesStateNotActive) {
		t.Fatalf("commit error = %v, want errResponsesStateNotActive", err)
	}
	messages, err := session.BuildMessagesWithToolInteractionsWithManager(context.Background(), server.responsesState.manager, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractionsWithManager() error = %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("session messages = %+v, want no appended messages", messages)
	}
	stored, ok, err := server.responsesState.loadResponse(respID)
	if err != nil || !ok || stored.Status != "cancelled" {
		t.Fatalf("stored response = rec:%#v ok:%v err:%v, want cancelled", stored, ok, err)
	}
}

func TestOpenAIResponsesConversationCommitForksStalePreparedHead(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientForTesting(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger)
	server := NewTestServerDirectWithClient(t, config, client)
	conv, _, err := server.responsesState.createConversation(nil, "")
	if err != nil {
		t.Fatalf("createConversation() error = %v", err)
	}

	firstReq := &OpenAIResponsesRequest{Model: "claude-test", Conversation: conv.ID, Input: "first question"}
	firstTyped := TypedRequest{Messages: []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), "first question")}}
	firstState, _, err := server.prepareOpenAIResponsesState(context.Background(), firstReq, firstTyped, "claude-test", false)
	if err != nil {
		t.Fatalf("prepare first state error = %v", err)
	}
	secondReq := &OpenAIResponsesRequest{Model: "claude-test", Conversation: conv.ID, Input: "second question"}
	secondTyped := TypedRequest{Messages: []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), "second question")}}
	secondState, _, err := server.prepareOpenAIResponsesState(context.Background(), secondReq, secondTyped, "claude-test", false)
	if err != nil {
		t.Fatalf("prepare second state error = %v", err)
	}

	firstResp := &OpenAIResponsesResponse{
		ID:        "resp_concurrent_first",
		Object:    "response",
		Status:    "completed",
		Model:     "claude-test",
		CreatedAt: time.Now().Unix(),
		Output: []OpenAIResponsesOutputItem{{
			Type:   "message",
			Status: "completed",
			Role:   core.RoleAssistant,
			Content: []OpenAIResponsesContentPart{{
				Type: "output_text",
				Text: "first answer",
			}},
		}},
	}
	if err := server.commitOpenAIResponsesStateWithBlocks(context.Background(), firstState, firstReq, firstTyped, firstResp, "claude-test", nil); err != nil {
		t.Fatalf("commit first response error = %v", err)
	}

	secondResp := &OpenAIResponsesResponse{
		ID:        "resp_concurrent_second",
		Object:    "response",
		Status:    "completed",
		Model:     "claude-test",
		CreatedAt: time.Now().Unix(),
		Output: []OpenAIResponsesOutputItem{{
			Type:   "message",
			Status: "completed",
			Role:   core.RoleAssistant,
			Content: []OpenAIResponsesContentPart{{
				Type: "output_text",
				Text: "second answer",
			}},
		}},
	}
	if err := server.commitOpenAIResponsesStateWithBlocks(context.Background(), secondState, secondReq, secondTyped, secondResp, "claude-test", nil); err != nil {
		t.Fatalf("commit second response error = %v", err)
	}

	convAfter, ok, err := server.responsesState.loadConversation(conv.ID)
	if err != nil || !ok {
		t.Fatalf("load conversation after commits = ok:%v err:%v", ok, err)
	}
	if convAfter.LastResponseID != firstResp.ID {
		t.Fatalf("conversation head = %q, want first response %q", convAfter.LastResponseID, firstResp.ID)
	}
	firstRec, ok, err := server.responsesState.loadResponse(firstResp.ID)
	if err != nil || !ok {
		t.Fatalf("load first response = ok:%v err:%v", ok, err)
	}
	secondRec, ok, err := server.responsesState.loadResponse(secondResp.ID)
	if err != nil || !ok {
		t.Fatalf("load second response = ok:%v err:%v", ok, err)
	}
	if firstRec.SessionPath != convAfter.SessionPath {
		t.Fatalf("first response session = %q, want conversation head path %q", firstRec.SessionPath, convAfter.SessionPath)
	}
	if secondRec.SessionPath == convAfter.SessionPath {
		t.Fatalf("stale second response overwrote conversation head path %q", convAfter.SessionPath)
	}
	items := getJSON(t, server, "/v1/conversations/"+conv.ID+"/items")
	itemDump := fmt.Sprint(items)
	if !strings.Contains(itemDump, "first question") || !strings.Contains(itemDump, "first answer") {
		t.Fatalf("conversation head items missing first response: %#v", items)
	}
	if strings.Contains(itemDump, "second question") || strings.Contains(itemDump, "second answer") {
		t.Fatalf("stale response leaked into conversation head items: %#v", items)
	}
	staleMessages, err := session.BuildMessagesWithToolInteractionsWithManager(context.Background(), server.responsesState.manager, secondRec.SessionPath)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractionsWithManager(stale) error = %v", err)
	}
	staleDump := fmt.Sprint(staleMessages)
	if !strings.Contains(staleDump, "second question") || !strings.Contains(staleDump, "second answer") {
		t.Fatalf("stale fork missing second response messages: %+v", staleMessages)
	}
	if strings.Contains(staleDump, "first question") || strings.Contains(staleDump, "first answer") {
		t.Fatalf("stale fork copied concurrent first response: %+v", staleMessages)
	}
}

func TestCompactedResponseOutputItemsSkipsHistoricalAssistantByMessageIndex(t *testing.T) {
	history := []core.TypedMessage{{
		Role: string(core.RoleAssistant),
		Blocks: []core.Block{
			core.ReasoningBlock{
				Provider: "openai",
				Type:     "reasoning",
				ID:       "rs_history",
				Raw:      json.RawMessage(`{"type":"reasoning","id":"rs_history","status":"completed"}`),
			},
			core.TextBlock{Text: "old assistant text"},
		},
	}}
	current := core.NewTextMessage(string(core.RoleUser), "current user text")
	output := compactedResponseOutputItems(&openAIResponsesStateContext{History: history}, append(history, current), "compact summary")

	if len(output) != 2 {
		t.Fatalf("output = %+v, want current user message plus compaction", output)
	}
	if output[0].Type != "message" || output[0].Role != core.RoleUser {
		t.Fatalf("first output = %+v, want current user message", output[0])
	}
	if len(output[0].Content) != 1 || output[0].Content[0].Text != "current user text" {
		t.Fatalf("first output content = %+v, want current user text", output[0].Content)
	}
	if output[1].Type != "compaction" || output[1].EncryptedContent != "compact summary" {
		t.Fatalf("second output = %+v, want compaction", output[1])
	}
	for _, item := range output {
		for _, part := range item.Content {
			if strings.Contains(part.Text, "old assistant text") {
				t.Fatalf("compaction output replayed historical assistant text: %+v", output)
			}
		}
	}
}

func TestConversationHistoryDeletionPreservesHiddenReasoningWithRemainingVisibleItem(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{SessionsDir: t.TempDir()})
	conv, sess, err := server.responsesState.createConversation(nil, "")
	if err != nil {
		t.Fatalf("createConversation() error = %v", err)
	}

	msg := core.TypedMessage{
		Role: string(core.RoleAssistant),
		Blocks: []core.Block{
			core.ReasoningBlock{
				Provider:  "anthropic",
				Type:      "thinking",
				Text:      "hidden reasoning",
				Signature: "sig_hidden",
				Raw:       json.RawMessage(`{"type":"thinking","thinking":"hidden reasoning","signature":"sig_hidden"}`),
			},
			core.TextBlock{Text: "visible text"},
			core.ToolUseBlock{
				ID:    "call_visible",
				Name:  "lookup",
				Input: json.RawMessage(`{"q":"x"}`),
			},
		},
	}
	if err := appendTypedMessageToSession(context.Background(), sess, msg, "claude-test"); err != nil {
		t.Fatalf("appendTypedMessageToSession() error = %v", err)
	}
	conv.SessionPath = sess.Path

	conv.DeletedItemIDs = []string{"item_0000"}
	history, err := server.conversationHistory(context.Background(), conv)
	if err != nil {
		t.Fatalf("conversationHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1: %#v", len(history), history)
	}
	if len(history[0].Blocks) != 2 {
		t.Fatalf("assistant blocks after deleting text = %#v, want reasoning plus tool_use", history[0].Blocks)
	}
	reasoning, ok := history[0].Blocks[0].(core.ReasoningBlock)
	if !ok || reasoning.Provider != "anthropic" || reasoning.Signature != "sig_hidden" {
		t.Fatalf("first block = %#v, want preserved Anthropic reasoning", history[0].Blocks[0])
	}
	if toolUse, ok := history[0].Blocks[1].(core.ToolUseBlock); !ok || toolUse.ID != "call_visible" {
		t.Fatalf("second block = %#v, want remaining tool_use", history[0].Blocks[1])
	}

	conv.DeletedItemIDs = []string{"item_0000", "item_0001"}
	history, err = server.conversationHistory(context.Background(), conv)
	if err != nil {
		t.Fatalf("conversationHistory(all deleted) error = %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history length after deleting all visible items = %d, want 0: %#v", len(history), history)
	}
}

func TestOpenAIResponsesStatePreviousResponseLifecycle(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/messages" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		text := "first answer"
		if callIndex == 2 {
			text = "second answer"
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: text,
			}},
			Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 3},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model": "claude-test",
		"input": "hello",
	})
	respID, _ := firstResp["id"].(string)
	if !strings.HasPrefix(respID, "resp_") {
		t.Fatalf("response id = %q, want resp_ prefix", respID)
	}

	secondResp := postResponses(t, server, map[string]interface{}{
		"model":                "claude-test",
		"previous_response_id": respID,
		"input":                "next",
	})
	if got, _ := lookupStatefulJSONPath(secondResp, "output.0.content.0.text"); got != "second answer" {
		t.Fatalf("second output text = %q, want second answer", got)
	}

	mu.Lock()
	if len(backendBodies) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(backendBodies))
	}
	secondBody := string(backendBodies[1])
	mu.Unlock()
	for _, want := range []string{"hello", "first answer", "next"} {
		if !strings.Contains(secondBody, want) {
			t.Fatalf("second backend request does not contain %q: %s", want, secondBody)
		}
	}

	retrieved := getJSON(t, server, "/v1/responses/"+respID)
	if got, _ := retrieved["id"].(string); got != respID {
		t.Fatalf("retrieved id = %q, want %q", got, respID)
	}
	inputItems := getJSON(t, server, "/v1/responses/"+respID+"/input_items")
	data, _ := inputItems["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("input item count = %d, want 1: %#v", len(data), data)
	}

	deleteResp := requestJSON(t, server, http.MethodDelete, "/v1/responses/"+respID, nil)
	if deleted, _ := deleteResp["deleted"].(bool); !deleted {
		t.Fatalf("delete response = %#v, want deleted=true", deleteResp)
	}
}

func TestOpenAIResponsesConversationReplayPreservesAnthropicSignedThinking(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/messages" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		if callIndex == 1 {
			return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
				ID:         "msg_backend_tool",
				Type:       "message",
				Role:       core.RoleAssistant,
				Model:      "claude-test",
				StopReason: "tool_use",
				Content: []AnthropicContentBlock{
					{
						Type:      "thinking",
						Thinking:  "I should inspect the tool result.",
						Signature: "sig_1",
					},
					{
						Type: "text",
						Text: "I will use a tool.",
					},
					{
						Type:  "tool_use",
						ID:    "call_weather",
						Name:  "lookup_weather",
						Input: map[string]interface{}{"city": "Paris"},
					},
				},
				Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 5},
			}), nil
		}

		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend_after_tool",
			Type:       "message",
			Role:       core.RoleAssistant,
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "The tool result was replayed.",
			}},
			Usage: &AnthropicUsage{InputTokens: 12, OutputTokens: 4},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": "auto",
		"input":        "check the weather",
	})
	convID := responseConversationID(firstResp)
	if convID == "" {
		t.Fatalf("first response missing conversation id: %#v", firstResp)
	}

	secondResp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": convID,
		"input": []interface{}{map[string]interface{}{
			"type":    "function_call_output",
			"call_id": "call_weather",
			"output":  "sunny",
		}},
	})
	if got, _ := lookupStatefulJSONPath(secondResp, "output.0.content.0.text"); got != "The tool result was replayed." {
		t.Fatalf("second output text = %#v, want replay response", got)
	}

	mu.Lock()
	if len(backendBodies) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(backendBodies))
	}
	secondBody := string(backendBodies[1])
	mu.Unlock()
	for _, want := range []string{
		`"type":"thinking"`,
		`"signature":"sig_1"`,
		`"type":"tool_use"`,
		`"id":"call_weather"`,
		`"type":"tool_result"`,
		`"tool_use_id":"call_weather"`,
	} {
		if !strings.Contains(secondBody, want) {
			t.Fatalf("second backend request missing %s: %s", want, secondBody)
		}
	}

	items := getJSON(t, server, "/v1/conversations/"+convID+"/items?order=asc")
	itemDump := fmt.Sprint(items)
	if strings.Contains(itemDump, "sig_1") || strings.Contains(itemDump, "thinking") {
		t.Fatalf("conversation items exposed hidden thinking metadata: %#v", items)
	}
	if !strings.Contains(itemDump, "function_call") {
		t.Fatalf("conversation items missing visible function_call item: %#v", items)
	}
}

func TestOpenAIResponsesPreviousResponseBranchesFromSnapshot(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/messages" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		text := "root answer"
		switch callIndex {
		case 2:
			text = "branch one answer"
		case 3:
			text = "branch two answer"
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: text,
			}},
			Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 3},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model": "claude-test",
		"input": "root question",
	})
	firstID, _ := firstResp["id"].(string)
	if firstID == "" {
		t.Fatalf("first response id missing: %#v", firstResp)
	}

	_ = postResponses(t, server, map[string]interface{}{
		"model":                "claude-test",
		"previous_response_id": firstID,
		"input":                "branch one",
	})
	_ = postResponses(t, server, map[string]interface{}{
		"model":                "claude-test",
		"previous_response_id": firstID,
		"input":                "branch two",
	})

	mu.Lock()
	if len(backendBodies) != 3 {
		t.Fatalf("backend calls = %d, want 3", len(backendBodies))
	}
	thirdBody := string(backendBodies[2])
	mu.Unlock()
	for _, want := range []string{"root question", "root answer", "branch two"} {
		if !strings.Contains(thirdBody, want) {
			t.Fatalf("third backend request missing %q: %s", want, thirdBody)
		}
	}
	for _, unwanted := range []string{"branch one", "branch one answer"} {
		if strings.Contains(thirdBody, unwanted) {
			t.Fatalf("third backend request unexpectedly contains %q: %s", unwanted, thirdBody)
		}
	}
}

func TestOpenAIResponsesConversationPreviousResponseUsesResponseSnapshot(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/messages" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		text := "first conversation answer"
		switch callIndex {
		case 2:
			text = "second conversation answer"
		case 3:
			text = "branch from first answer"
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: text,
			}},
			Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 3},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": "auto",
		"input":        "first conversation question",
	})
	convID := responseConversationID(firstResp)
	firstID, _ := firstResp["id"].(string)
	if convID == "" || firstID == "" {
		t.Fatalf("first response missing ids: %#v", firstResp)
	}
	secondResp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": convID,
		"input":        "second conversation question",
	})
	secondID, _ := secondResp["id"].(string)
	if secondID == "" {
		t.Fatalf("second response missing id: %#v", secondResp)
	}
	branchResp := postResponses(t, server, map[string]interface{}{
		"model":                "claude-test",
		"conversation":         convID,
		"previous_response_id": firstID,
		"input":                "branch from first question",
	})
	branchID, _ := branchResp["id"].(string)
	if branchID == "" {
		t.Fatalf("branch response missing id: %#v", branchResp)
	}

	mu.Lock()
	if len(backendBodies) != 3 {
		t.Fatalf("backend calls = %d, want 3", len(backendBodies))
	}
	branchBody := string(backendBodies[2])
	mu.Unlock()
	for _, want := range []string{"first conversation question", "first conversation answer", "branch from first question"} {
		if !strings.Contains(branchBody, want) {
			t.Fatalf("branch backend request missing %q: %s", want, branchBody)
		}
	}
	for _, unwanted := range []string{"second conversation question", "second conversation answer"} {
		if strings.Contains(branchBody, unwanted) {
			t.Fatalf("branch backend request unexpectedly contains %q: %s", unwanted, branchBody)
		}
	}

	convAfter, ok, err := server.responsesState.loadConversation(convID)
	if err != nil || !ok {
		t.Fatalf("load conversation after branch = ok:%v err:%v", ok, err)
	}
	if convAfter.LastResponseID != secondID {
		t.Fatalf("conversation head = %q, want second response %q", convAfter.LastResponseID, secondID)
	}
	branchRec, ok, err := server.responsesState.loadResponse(branchID)
	if err != nil || !ok {
		t.Fatalf("load branch response = ok:%v err:%v", ok, err)
	}
	if branchRec.ConversationID != convID {
		t.Fatalf("branch conversation id = %q, want %q", branchRec.ConversationID, convID)
	}
	if branchRec.SessionPath == convAfter.SessionPath {
		t.Fatalf("older previous_response_id branch advanced conversation head path %q", convAfter.SessionPath)
	}
}

func TestOpenAIResponsesStoreFalsePreviousResponseDoesNotWriteState(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		text := "stored answer"
		if callIndex == 2 {
			text = "transient answer"
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend_store_false",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: text,
			}},
			Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 3},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model": "claude-test",
		"input": "stored question",
	})
	storedID, _ := firstResp["id"].(string)

	transientResp := postResponses(t, server, map[string]interface{}{
		"model":                "claude-test",
		"previous_response_id": storedID,
		"input":                "transient question",
		"store":                false,
	})
	transientID, _ := transientResp["id"].(string)
	if transientID == "" {
		t.Fatalf("transient response missing id: %#v", transientResp)
	}

	mu.Lock()
	if len(backendBodies) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(backendBodies))
	}
	secondBody := string(backendBodies[1])
	mu.Unlock()
	for _, want := range []string{"stored question", "stored answer", "transient question"} {
		if !strings.Contains(secondBody, want) {
			t.Fatalf("store=false backend request missing %q: %s", want, secondBody)
		}
	}

	code, body := requestJSONStatus(t, server, http.MethodGet, "/v1/responses/"+transientID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET store=false response code = %d, want 404; body = %s", code, string(body))
	}
	storedItems := getJSON(t, server, "/v1/responses/"+storedID+"/input_items")
	data, _ := storedItems["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("stored input item count = %d, want 1: %#v", len(data), data)
	}
	if strings.Contains(fmt.Sprint(storedItems), "transient question") || strings.Contains(fmt.Sprint(storedItems), "transient answer") {
		t.Fatalf("store=false messages leaked into stored input items: %#v", storedItems)
	}
}

func TestOpenAIResponsesStoreFalseConversationDoesNotWriteState(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		text := "conversation answer"
		if callIndex == 2 {
			text = "transient conversation answer"
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend_conversation_store_false",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: text,
			}},
			Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 3},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	conv := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{
		"input": "conversation seed",
	})
	convID, _ := conv["id"].(string)
	if convID == "" {
		t.Fatalf("conversation missing id: %#v", conv)
	}
	firstResp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": convID,
		"input":        "stored conversation question",
	})
	if got := responseConversationID(firstResp); got != convID {
		t.Fatalf("stored conversation response conversation id = %q, want %q; payload = %#v", got, convID, firstResp)
	}
	storedID, _ := firstResp["id"].(string)
	rec, ok, err := server.responsesState.loadResponse(storedID)
	if err != nil || !ok || rec.ConversationID != convID {
		t.Fatalf("stored response record = rec:%#v ok:%v err:%v, want conversation id %q", rec, ok, err, convID)
	}

	transientResp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": convID,
		"input":        "transient conversation question",
		"store":        false,
	})
	transientID, _ := transientResp["id"].(string)
	if transientID == "" {
		t.Fatalf("transient conversation response missing id: %#v", transientResp)
	}
	if got := responseConversationID(transientResp); got != convID {
		t.Fatalf("transient conversation response conversation id = %q, want %q; payload = %#v", got, convID, transientResp)
	}

	mu.Lock()
	if len(backendBodies) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(backendBodies))
	}
	secondBody := string(backendBodies[1])
	mu.Unlock()
	for _, want := range []string{"conversation seed", "stored conversation question", "conversation answer", "transient conversation question"} {
		if !strings.Contains(secondBody, want) {
			t.Fatalf("store=false conversation backend request missing %q: %s", want, secondBody)
		}
	}

	code, body := requestJSONStatus(t, server, http.MethodGet, "/v1/responses/"+transientID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET store=false conversation response code = %d, want 404; body = %s", code, string(body))
	}
	convAfter, ok, err := server.responsesState.loadConversation(convID)
	if err != nil || !ok {
		t.Fatalf("load conversation after store=false = ok:%v err:%v", ok, err)
	}
	if convAfter.LastResponseID != storedID {
		t.Fatalf("conversation last response = %q, want stored response %q", convAfter.LastResponseID, storedID)
	}
	items := getJSON(t, server, "/v1/conversations/"+convID+"/items")
	if strings.Contains(fmt.Sprint(items), "transient conversation question") || strings.Contains(fmt.Sprint(items), "transient conversation answer") {
		t.Fatalf("store=false conversation messages leaked into items: %#v", items)
	}
}

func TestOpenAIResponsesStoreFalseAutoConversationDoesNotCreateState(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if !bytes.Contains(body, []byte("transient auto conversation")) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad body"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend_auto_store_false",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "transient auto answer",
			}},
			Usage: &AnthropicUsage{InputTokens: 4, OutputTokens: 2},
		}), nil
	})

	sessionsDir := t.TempDir()
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        sessionsDir,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"conversation": "auto",
		"input":        "transient auto conversation",
		"store":        false,
	})
	respID, _ := resp["id"].(string)
	if respID == "" {
		t.Fatalf("store=false auto conversation response missing id: %#v", resp)
	}

	code, body := requestJSONStatus(t, server, http.MethodGet, "/v1/responses/"+respID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET store=false auto response code = %d, want 404; body = %s", code, string(body))
	}
	entries, err := os.ReadDir(server.responsesState.conversationsDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(conversationsDir) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("store=false auto conversation created state files: %v", entries)
	}
	entries, err = os.ReadDir(server.responsesState.responsesDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(responsesDir) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("store=false auto conversation created response files: %v", entries)
	}
}

func TestOpenAIResponsesGeneratedConversationIDReturnedAndRetrieved(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend_generated_conversation",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "generated conversation answer",
			}},
			Usage: &AnthropicUsage{InputTokens: 5, OutputTokens: 2},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	tests := []struct {
		name         string
		conversation interface{}
	}{
		{name: "auto", conversation: "auto"},
		{name: "object_without_id", conversation: map[string]interface{}{"metadata": map[string]interface{}{"source": "test"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postResponses(t, server, map[string]interface{}{
				"model":        "claude-test",
				"conversation": tt.conversation,
				"input":        "start generated conversation",
			})
			respID, _ := resp["id"].(string)
			if respID == "" {
				t.Fatalf("response missing id: %#v", resp)
			}
			convID := responseConversationID(resp)
			if convID == "" {
				t.Fatalf("response missing generated conversation id: %#v", resp)
			}

			rec, ok, err := server.responsesState.loadResponse(respID)
			if err != nil || !ok || rec.ConversationID != convID {
				t.Fatalf("stored response record = rec:%#v ok:%v err:%v, want conversation id %q", rec, ok, err, convID)
			}

			retrieved := getJSON(t, server, "/v1/responses/"+respID)
			if got := responseConversationID(retrieved); got != convID {
				t.Fatalf("retrieved response conversation id = %q, want %q; payload = %#v", got, convID, retrieved)
			}
		})
	}
}

func TestOpenAIResponsesResponseMetadataDoesNotOverwriteConversationMetadata(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend_metadata",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "metadata answer",
			}},
			Usage: &AnthropicUsage{InputTokens: 5, OutputTokens: 2},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	t.Run("created conversation metadata survives response metadata", func(t *testing.T) {
		resp := postResponses(t, server, map[string]interface{}{
			"model": "claude-test",
			"conversation": map[string]interface{}{
				"metadata": map[string]interface{}{"scope": "conversation"},
			},
			"metadata": map[string]interface{}{"scope": "response"},
			"input":    "start metadata conversation",
		})
		convID := responseConversationID(resp)
		if convID == "" {
			t.Fatalf("response missing conversation id: %#v", resp)
		}
		assertConversationMetadataScope(t, server, convID, "conversation")
		assertResponseRecordMetadataScope(t, server, resp, "response")
	})

	t.Run("existing conversation metadata survives response metadata", func(t *testing.T) {
		conv := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{
			"metadata": map[string]interface{}{"scope": "existing"},
		})
		convID, _ := conv["id"].(string)
		if convID == "" {
			t.Fatalf("conversation missing id: %#v", conv)
		}

		resp := postResponses(t, server, map[string]interface{}{
			"model":        "claude-test",
			"conversation": convID,
			"metadata":     map[string]interface{}{"scope": "response"},
			"input":        "continue metadata conversation",
		})
		assertConversationMetadataScope(t, server, convID, "existing")
		assertResponseRecordMetadataScope(t, server, resp, "response")
	})

	t.Run("conversation object metadata update survives response metadata", func(t *testing.T) {
		conv := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{
			"metadata": map[string]interface{}{"scope": "initial"},
		})
		convID, _ := conv["id"].(string)
		if convID == "" {
			t.Fatalf("conversation missing id: %#v", conv)
		}

		resp := postResponses(t, server, map[string]interface{}{
			"model": "claude-test",
			"conversation": map[string]interface{}{
				"id":       convID,
				"metadata": map[string]interface{}{"scope": "updated"},
			},
			"metadata": map[string]interface{}{"scope": "response"},
			"input":    "update metadata conversation",
		})
		assertConversationMetadataScope(t, server, convID, "updated")
		assertResponseRecordMetadataScope(t, server, resp, "response")
	})
}

func TestOpenAIResponsesInputTokensReadOnlyAndPagination(t *testing.T) {
	var mu sync.Mutex
	var backendPaths []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendPaths = append(backendPaths, r.URL.Path)
		callIndex := len(backendPaths)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/messages/count_tokens") {
			if !bytes.Contains(body, []byte("first")) || !bytes.Contains(body, []byte("stored answer")) || !bytes.Contains(body, []byte("count only")) {
				return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad count body"}), nil
			}
			return jsonRoundTripResponse(http.StatusOK, AnthropicTokenCountResponse{InputTokens: 31}), nil
		}
		if callIndex != 1 {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "unexpected generation request"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "stored answer",
			}},
			Usage: &AnthropicUsage{InputTokens: 8, OutputTokens: 2},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model": "claude-test",
		"input": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "first"},
			map[string]interface{}{"type": "message", "role": "user", "content": "second"},
			map[string]interface{}{"type": "message", "role": "user", "content": "third"},
		},
	})
	respID, _ := firstResp["id"].(string)

	countResp := requestJSON(t, server, http.MethodPost, "/v1/responses/input_tokens", map[string]interface{}{
		"model":                "claude-test",
		"previous_response_id": respID,
		"input":                "count only",
	})
	if countResp["object"] != "response.input_tokens" {
		t.Fatalf("input_tokens object = %#v", countResp["object"])
	}
	if got, _ := countResp["input_tokens"].(float64); got != 31 {
		t.Fatalf("input_tokens = %v, want 31", countResp["input_tokens"])
	}

	mu.Lock()
	if len(backendPaths) != 2 || !strings.HasSuffix(backendPaths[1], "/messages/count_tokens") {
		t.Fatalf("backend paths after input_tokens = %#v, want generation then count_tokens", backendPaths)
	}
	mu.Unlock()

	firstPage := getJSON(t, server, "/v1/responses/"+respID+"/input_items?order=asc&limit=1")
	if got, _ := lookupStatefulJSONPath(firstPage, "data.0.id"); got != "item_0000" {
		t.Fatalf("asc first page id = %#v, want item_0000", got)
	}
	if firstPage["has_more"] != true {
		t.Fatalf("asc first page has_more = %#v, want true", firstPage["has_more"])
	}
	secondPage := getJSON(t, server, "/v1/responses/"+respID+"/input_items?order=asc&after=item_0000&limit=1")
	if got, _ := lookupStatefulJSONPath(secondPage, "data.0.id"); got != "item_0001" {
		t.Fatalf("asc second page id = %#v, want item_0001", got)
	}
	defaultPage := getJSON(t, server, "/v1/responses/"+respID+"/input_items?limit=1")
	if got, _ := lookupStatefulJSONPath(defaultPage, "data.0.id"); got != "item_0002" {
		t.Fatalf("default desc page id = %#v, want item_0002", got)
	}
}

func TestOpenAIResponsesInputTokensUsesAnthropicNativeCount(t *testing.T) {
	var mu sync.Mutex
	var backendPaths []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendPaths = append(backendPaths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/messages/count_tokens" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad key"}), nil
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad version"}), nil
		}
		if !bytes.Contains(body, []byte(`"model":"claude-test"`)) || !bytes.Contains(body, []byte("count natively")) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad body"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicTokenCountResponse{InputTokens: 77}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local/v1",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses/input_tokens", map[string]interface{}{
		"model": "claude-test",
		"input": "count natively",
	})
	if resp["object"] != "response.input_tokens" {
		t.Fatalf("object = %#v, want response.input_tokens", resp["object"])
	}
	if got, _ := resp["input_tokens"].(float64); got != 77 {
		t.Fatalf("input_tokens = %v, want 77", resp["input_tokens"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(backendPaths) != 1 || backendPaths[0] != "/v1/messages/count_tokens" {
		t.Fatalf("backend paths = %#v, want one count_tokens call", backendPaths)
	}
}

func TestOpenAIResponsesInputTokensUsesGoogleCountTokens(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	var backendPaths []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendPaths = append(backendPaths, r.URL.Path)
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		mu.Unlock()
		if r.URL.Path != "/v1beta/models/gemini-test:countTokens" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		if got := r.Header.Get("x-goog-api-key"); got != "google-key" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad key"}), nil
		}
		if !bytes.Contains(body, []byte(`"generateContentRequest"`)) || !bytes.Contains(body, []byte(`"model":"models/gemini-test"`)) || !bytes.Contains(body, []byte("count with gemini")) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad body"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, GoogleCountTokensResponse{TotalTokens: 88}), nil
	})

	config := &Config{
		Provider:           constants.ProviderGoogle,
		ProviderURL:        "http://google.local/v1beta/models",
		GoogleAPIKey:       "google-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses/input_tokens", map[string]interface{}{
		"model": "gemini-test",
		"input": "count with gemini",
	})
	if got, _ := resp["input_tokens"].(float64); got != 88 {
		t.Fatalf("input_tokens = %v, want 88", resp["input_tokens"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(backendPaths) != 1 || backendPaths[0] != "/v1beta/models/gemini-test:countTokens" {
		t.Fatalf("backend paths = %#v, want one Google countTokens call", backendPaths)
	}
}

func TestOpenAIResponsesInputTokensArgoNonClaudeUsesEstimate(t *testing.T) {
	var requestCount int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestCount++
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	})

	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local",
		ArgoAPIKey:         "argo-key",
		ArgoUser:           "fixture-user",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses/input_tokens", map[string]interface{}{
		"model": "gpt-test",
		"input": "estimate argo non claude",
	})
	if got, _ := resp["input_tokens"].(float64); got <= 0 {
		t.Fatalf("input_tokens = %v, want > 0", resp["input_tokens"])
	}
	if requestCount != 0 {
		t.Fatalf("backend request count = %d, want 0", requestCount)
	}
}

func TestOpenAIResponsesCompactCreatesContinuationState(t *testing.T) {
	var mu sync.Mutex
	var backendBodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		backendBodies = append(backendBodies, append([]byte(nil), body...))
		callIndex := len(backendBodies)
		mu.Unlock()

		text := "first answer"
		switch callIndex {
		case 2:
			text = "compacted alpha beta gamma"
		case 3:
			text = "continued from compacted state"
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: text,
			}},
			Usage: &AnthropicUsage{InputTokens: 10, OutputTokens: 4},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	firstResp := postResponses(t, server, map[string]interface{}{
		"model": "claude-test",
		"input": "Remember alpha and beta.",
	})
	firstID, _ := firstResp["id"].(string)

	compactResp := requestJSON(t, server, http.MethodPost, "/v1/responses/compact", map[string]interface{}{
		"model":                "claude-test",
		"previous_response_id": firstID,
		"input":                "The latest user asks for gamma.",
	})
	if compactResp["object"] != "response.compaction" {
		t.Fatalf("compact object = %#v", compactResp["object"])
	}
	compactID, _ := compactResp["id"].(string)
	if !strings.HasPrefix(compactID, "resp_") {
		t.Fatalf("compact id = %q, want resp_ prefix", compactID)
	}
	if got, _ := lookupStatefulJSONPath(compactResp, "output.2.type"); got != "compaction" {
		t.Fatalf("compact output type = %#v, want compaction", got)
	}
	encryptedContent, ok := lookupStatefulJSONPath(compactResp, "output.2.encrypted_content")
	if !ok {
		t.Fatalf("compact encrypted_content missing: %#v", compactResp)
	}

	followupResp := postResponses(t, server, map[string]interface{}{
		"model": "claude-test",
		"input": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "Remember alpha and beta."},
			map[string]interface{}{"type": "message", "role": "user", "content": "The latest user asks for gamma."},
			map[string]interface{}{"type": "compaction", "encrypted_content": encryptedContent},
			map[string]interface{}{"type": "message", "role": "user", "content": "Continue."},
		},
	})
	if got, _ := lookupStatefulJSONPath(followupResp, "output.0.content.0.text"); got != "continued from compacted state" {
		t.Fatalf("follow-up output = %#v, want compacted continuation", got)
	}

	mu.Lock()
	if len(backendBodies) != 3 {
		t.Fatalf("backend calls = %d, want 3", len(backendBodies))
	}
	followupBody := string(backendBodies[2])
	mu.Unlock()
	if !strings.Contains(followupBody, "compacted alpha beta gamma") {
		t.Fatalf("follow-up backend request missing compacted summary: %s", followupBody)
	}
	for _, unwanted := range []string{"first answer"} {
		if strings.Contains(followupBody, unwanted) {
			t.Fatalf("follow-up backend request unexpectedly contains %q: %s", unwanted, followupBody)
		}
	}
}

func TestOpenAIConversationItemsPagination(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	}))
	server := NewTestServerDirectWithClient(t, config, client)

	conv := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "one"},
			map[string]interface{}{"type": "message", "role": "user", "content": "two"},
			map[string]interface{}{"type": "message", "role": "user", "content": "three"},
		},
	})
	convID, _ := conv["id"].(string)

	firstPage := getJSON(t, server, "/v1/conversations/"+convID+"/items?order=asc&limit=2")
	if got, _ := lookupStatefulJSONPath(firstPage, "data.0.id"); got != "item_0000" {
		t.Fatalf("conversation first id = %#v, want item_0000", got)
	}
	if got, _ := lookupStatefulJSONPath(firstPage, "data.1.id"); got != "item_0001" {
		t.Fatalf("conversation second id = %#v, want item_0001", got)
	}
	if firstPage["has_more"] != true {
		t.Fatalf("conversation first page has_more = %#v, want true", firstPage["has_more"])
	}
	secondPage := getJSON(t, server, "/v1/conversations/"+convID+"/items?order=asc&after=item_0001&limit=2")
	if got, _ := lookupStatefulJSONPath(secondPage, "data.0.id"); got != "item_0002" {
		t.Fatalf("conversation next id = %#v, want item_0002", got)
	}
}

func TestOpenAIConversationAppendItemsReturnsOnlyAppendedItems(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	}))
	server := NewTestServerDirectWithClient(t, config, client)

	conv := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "seed"},
		},
	})
	convID, _ := conv["id"].(string)
	if convID == "" {
		t.Fatalf("conversation missing id: %#v", conv)
	}

	appended := requestJSON(t, server, http.MethodPost, "/v1/conversations/"+convID+"/items", map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "appended"},
		},
	})
	data, ok := appended["data"].([]interface{})
	if !ok || len(data) != 1 {
		t.Fatalf("append response data = %#v, want one appended item", appended["data"])
	}
	if got, _ := lookupStatefulJSONPath(appended, "data.0.id"); got != "item_0001" {
		t.Fatalf("appended item id = %#v, want item_0001", got)
	}
	if got, _ := lookupStatefulJSONPath(appended, "data.0.content.0.text"); got != "appended" {
		t.Fatalf("appended item text = %#v, want appended", got)
	}

	items := getJSON(t, server, "/v1/conversations/"+convID+"/items?order=asc")
	allData, ok := items["data"].([]interface{})
	if !ok || len(allData) != 2 {
		t.Fatalf("conversation items data = %#v, want two persisted items", items["data"])
	}
	if got, _ := lookupStatefulJSONPath(items, "data.0.id"); got != "item_0000" {
		t.Fatalf("first persisted item id = %#v, want item_0000", got)
	}
	if got, _ := lookupStatefulJSONPath(items, "data.1.id"); got != "item_0001" {
		t.Fatalf("second persisted item id = %#v, want item_0001", got)
	}
}

func TestOpenAIConversationCreateRejectsMalformedItemsWithoutState(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{
		Provider:           constants.ProviderAnthropic,
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	})

	tests := []struct {
		name    string
		payload map[string]interface{}
	}{
		{
			name:    "items object",
			payload: map[string]interface{}{"items": map[string]interface{}{"type": "message", "role": "user", "content": "bad"}},
		},
		{
			name:    "input item scalar",
			payload: map[string]interface{}{"input": []interface{}{"bad"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/conversations", tt.payload)
			if status != http.StatusBadRequest {
				t.Fatalf("create conversation status = %d, want 400; body = %s", status, string(body))
			}
			entries, err := os.ReadDir(server.responsesState.conversationsDir)
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("ReadDir(conversations) error = %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("conversation records = %d, want none after failed create", len(entries))
			}
		})
	}
}

func TestOpenAIConversationAppendSurfacesSaveStateError(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	}))
	server := NewTestServerDirectWithClient(t, config, client)

	conv := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "seed"},
		},
	})
	convID, _ := conv["id"].(string)
	if convID == "" {
		t.Fatalf("conversation missing id: %#v", conv)
	}

	payload, err := json.Marshal(map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "append after delete"},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal(payload) error = %v", err)
	}

	body := newBlockingRequestBody(payload)
	released := false
	defer func() {
		if !released {
			close(body.release)
		}
	}()

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+convID+"/items", body)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(recorder, req)
	}()

	waitForChannel(t, body.started, "append request body read")
	if _, ok, err := server.responsesState.deleteConversation(convID); err != nil || !ok {
		t.Fatalf("deleteConversation(%q) = ok:%v err:%v, want ok", convID, ok, err)
	}
	close(body.release)
	released = true
	waitForChannel(t, done, "append request completion")

	resp := recorder.Result()
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("append after delete status = %d, want 500; body = %s", resp.StatusCode, string(respBody))
	}
	var decoded OpenAIError
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(error) error = %v, body = %s", err, string(respBody))
	}
	if decoded.Error.Type != ErrTypeServer {
		t.Fatalf("error type = %q, want %q; body = %s", decoded.Error.Type, ErrTypeServer, string(respBody))
	}
	if decoded.Error.Code != "state_error" {
		t.Fatalf("error code = %q, want state_error; body = %s", decoded.Error.Code, string(respBody))
	}
	if !strings.Contains(decoded.Error.Message, errResponsesStateDeleted.Error()) {
		t.Fatalf("error message = %q, want %q", decoded.Error.Message, errResponsesStateDeleted.Error())
	}
}

func TestOpenAIResponsesCollectionUtilityRoutesPassThrough(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies [][]byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read backend body: %v", err)
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/responses/input_tokens":
			return jsonRoundTripResponse(http.StatusOK, OpenAIResponsesInputTokensResponse{Object: "response.input_tokens", InputTokens: 7}), nil
		case "/v1/responses/compact":
			return jsonRoundTripResponse(http.StatusOK, OpenAIResponsesCompactionResponse{ID: "resp_upstream", Object: "response.compaction", CreatedAt: 1, Output: []OpenAIResponsesOutputItem{}}), nil
		default:
			return jsonRoundTripResponse(http.StatusNotFound, map[string]interface{}{"error": "bad path"}), nil
		}
	})

	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		ModelMapRules:      []ModelMapRule{{Pattern: "^claude-3-haiku-20240307$", Model: "gpt-small"}, {Pattern: "^claude-3-5-sonnet-20241022$", Model: "gpt-big"}},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	countResp := requestJSON(t, server, http.MethodPost, "/v1/responses/input_tokens", map[string]interface{}{"model": "claude-3-haiku-20240307", "input": "count"})
	if countResp["object"] != "response.input_tokens" {
		t.Fatalf("input_tokens passthrough object = %#v", countResp["object"])
	}
	compactResp := requestJSON(t, server, http.MethodPost, "/v1/responses/compact", map[string]interface{}{"model": "claude-3-5-sonnet-20241022", "input": "compact"})
	if compactResp["object"] != "response.compaction" {
		t.Fatalf("compact passthrough object = %#v", compactResp["object"])
	}

	mu.Lock()
	defer mu.Unlock()
	wantPaths := []string{"/v1/responses/input_tokens", "/v1/responses/compact"}
	if len(paths) != len(wantPaths) {
		t.Fatalf("passthrough paths = %#v, want %#v", paths, wantPaths)
	}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Fatalf("passthrough path %d = %q, want %q", i, paths[i], wantPaths[i])
		}
	}
	wantBodies := []string{`"model":"gpt-small"`, `"model":"gpt-big"`}
	if len(bodies) != len(wantBodies) {
		t.Fatalf("passthrough bodies = %d, want %d", len(bodies), len(wantBodies))
	}
	for i, want := range wantBodies {
		if !bytes.Contains(bodies[i], []byte(want)) {
			t.Fatalf("passthrough body %d = %s, want %s", i, string(bodies[i]), want)
		}
	}
}

func TestOpenAIRawLifecyclePassThroughEnforcesRequestBodyLimit(t *testing.T) {
	var calls int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"ok": true}), nil
	})

	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		OpenAIAPIKey:       "test-key",
		MaxRequestBodySize: 32,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	for _, path := range []string{"/v1/responses/input_tokens", "/v1/responses/compact", "/v1/conversations"} {
		body := []byte(`{"model":"gpt-test","input":"` + strings.Repeat("x", 64) + `"}`)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		resp := recorder.Result()
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400; body = %s", path, resp.StatusCode, string(respBody))
		}
		if !bytes.Contains(respBody, []byte("request body exceeds maximum size")) {
			t.Fatalf("%s body = %s, want request body limit error", path, string(respBody))
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls)
	}
}

func TestOpenAIResponsesRejectsUnfinishedPreviousResponse(t *testing.T) {
	var upstreamCalls int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamCalls++
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	for _, status := range []string{"queued", "in_progress", "failed", "cancelled", "incomplete"} {
		respID := "resp_" + strings.ReplaceAll(status, "_", "")
		if err := server.responsesState.saveResponse(&responseRecord{
			Version:   responsesStateVersion,
			ID:        respID,
			Object:    "response",
			Status:    status,
			Model:     "claude-test",
			CreatedAt: time.Now().Unix(),
			Store:     true,
		}); err != nil {
			t.Fatalf("saveResponse(%s) error = %v", status, err)
		}

		code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
			"model":                "claude-test",
			"previous_response_id": respID,
			"input":                "next",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("status %q response code = %d, want 400; body = %s", status, code, string(body))
		}
	}

	respID := "resp_readonlyqueued"
	if err := server.responsesState.saveResponse(&responseRecord{
		Version:   responsesStateVersion,
		ID:        respID,
		Object:    "response",
		Status:    "queued",
		Model:     "claude-test",
		CreatedAt: time.Now().Unix(),
		Store:     true,
	}); err != nil {
		t.Fatalf("saveResponse(read-only) error = %v", err)
	}
	code, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses/input_tokens", map[string]interface{}{
		"previous_response_id": respID,
		"input":                "count",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("input_tokens code = %d, want 400; body = %s", code, string(body))
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestOpenAIResponsesBackgroundDeletePreservesResponseTombstone(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postResponses(t, server, map[string]interface{}{
		"model":      "claude-test",
		"input":      "hello",
		"background": true,
	})
	respID, _ := resp["id"].(string)
	if respID == "" {
		t.Fatalf("background response missing id: %#v", resp)
	}
	waitForChannel(t, started, "background upstream request")

	deleteResp := requestJSON(t, server, http.MethodDelete, "/v1/responses/"+respID, nil)
	if deleted, _ := deleteResp["deleted"].(bool); !deleted {
		t.Fatalf("delete response = %#v, want deleted=true", deleteResp)
	}
	waitForBackgroundIdle(t, server)

	code, body := requestJSONStatus(t, server, http.MethodGet, "/v1/responses/"+respID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET deleted background response code = %d, want 404; body = %s", code, string(body))
	}
	rec, ok, err := server.responsesState.loadResponse(respID)
	if err != nil || !ok || !rec.Deleted {
		t.Fatalf("stored response tombstone = rec:%#v ok:%v err:%v, want deleted record", rec, ok, err)
	}
}

func TestOpenAIResponsesBackgroundCompletionPreservesCreatedAt(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/messages" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		once.Do(func() { close(started) })
		<-release
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:         "msg_backend",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-test",
			StopReason: "end_turn",
			Content: []AnthropicContentBlock{{
				Type: "text",
				Text: "done",
			}},
			Usage: &AnthropicUsage{InputTokens: 3, OutputTokens: 1},
		}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postResponses(t, server, map[string]interface{}{
		"model":      "claude-test",
		"input":      "hello",
		"background": true,
	})
	respID, _ := resp["id"].(string)
	if respID == "" {
		t.Fatalf("background response missing id: %#v", resp)
	}
	waitForChannel(t, started, "background upstream request")

	const preservedCreatedAt int64 = 1234567890
	rec, ok, err := server.responsesState.loadResponse(respID)
	if err != nil || !ok {
		t.Fatalf("background response record = rec:%#v ok:%v err:%v", rec, ok, err)
	}
	rec.CreatedAt = preservedCreatedAt
	rec.Raw = mustMarshalJSON(&OpenAIResponsesResponse{
		ID:        respID,
		Object:    "response",
		CreatedAt: preservedCreatedAt,
		Status:    rec.Status,
		Model:     "claude-test",
		Output:    []OpenAIResponsesOutputItem{},
	})
	if err := server.responsesState.saveResponse(rec); err != nil {
		t.Fatalf("saveResponse(preserved created_at) error = %v", err)
	}

	close(release)
	waitForBackgroundIdle(t, server)

	retrieved := getJSON(t, server, "/v1/responses/"+respID)
	gotCreatedAt, _ := retrieved["created_at"].(float64)
	if int64(gotCreatedAt) != preservedCreatedAt {
		t.Fatalf("retrieved created_at = %.0f, want %d; payload = %#v", gotCreatedAt, preservedCreatedAt, retrieved)
	}
	rec, ok, err = server.responsesState.loadResponse(respID)
	if err != nil || !ok || rec.CreatedAt != preservedCreatedAt {
		t.Fatalf("completed response record = rec:%#v ok:%v err:%v, want created_at %d", rec, ok, err, preservedCreatedAt)
	}
}

func TestOpenAIResponsesBackgroundConversationDeleteDoesNotCompleteResponse(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postResponses(t, server, map[string]interface{}{
		"model":        "claude-test",
		"input":        "hello",
		"background":   true,
		"conversation": "auto",
	})
	respID, _ := resp["id"].(string)
	if respID == "" {
		t.Fatalf("background response missing id: %#v", resp)
	}
	convID := responseConversationID(resp)
	if convID == "" {
		t.Fatalf("background response missing conversation id: %#v", resp)
	}
	waitForChannel(t, started, "background upstream request")

	rec, ok, err := server.responsesState.loadResponse(respID)
	if err != nil || !ok || rec.ConversationID != convID {
		t.Fatalf("background response record = rec:%#v ok:%v err:%v, want conversation id %q", rec, ok, err, convID)
	}
	responseBeforeDelete := getJSON(t, server, "/v1/responses/"+respID)
	if got := responseConversationID(responseBeforeDelete); got != convID {
		t.Fatalf("retrieved background response conversation id = %q, want %q; payload = %#v", got, convID, responseBeforeDelete)
	}
	deleteResp := requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+convID, nil)
	if deleted, _ := deleteResp["deleted"].(bool); !deleted {
		t.Fatalf("delete conversation = %#v, want deleted=true", deleteResp)
	}
	waitForBackgroundIdle(t, server)

	code, body := requestJSONStatus(t, server, http.MethodGet, "/v1/conversations/"+convID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET deleted conversation code = %d, want 404; body = %s", code, string(body))
	}
	responsePayload := getJSON(t, server, "/v1/responses/"+respID)
	if responsePayload["status"] == "completed" {
		t.Fatalf("background response completed after conversation delete: %#v", responsePayload)
	}
}

func TestResponsesStateDeleteRejectsInvalidIDs(t *testing.T) {
	state := newResponsesState(t.TempDir())
	invalidIDs := []string{"../escape", `resp_bad\escape`}

	for _, id := range invalidIDs {
		if rec, ok, err := state.deleteResponse(id); err == nil || !strings.Contains(err.Error(), "invalid id") || ok || rec != nil {
			t.Fatalf("deleteResponse(%q) = rec:%#v ok:%v err:%v, want invalid id", id, rec, ok, err)
		}
		if rec, ok, err := state.deleteConversation(id); err == nil || !strings.Contains(err.Error(), "invalid id") || ok || rec != nil {
			t.Fatalf("deleteConversation(%q) = rec:%#v ok:%v err:%v, want invalid id", id, rec, ok, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonRoundTripResponse(status int, payload interface{}) *http.Response {
	data, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func postResponses(t *testing.T, server http.Handler, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	return requestJSON(t, server, http.MethodPost, "/v1/responses", payload)
}

func getJSON(t *testing.T, server http.Handler, path string) map[string]interface{} {
	t.Helper()
	return requestJSON(t, server, http.MethodGet, path, nil)
}

func responseConversationID(payload map[string]interface{}) string {
	conv, _ := payload["conversation"].(map[string]interface{})
	id, _ := conv["id"].(string)
	return id
}

func assertConversationMetadataScope(t *testing.T, server http.Handler, convID, want string) {
	t.Helper()
	conv := getJSON(t, server, "/v1/conversations/"+convID)
	metadata, ok := conv["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("conversation metadata = %#v, want object", conv["metadata"])
	}
	if got := metadata["scope"]; got != want {
		t.Fatalf("conversation metadata scope = %#v, want %q; payload = %#v", got, want, conv)
	}
}

func assertResponseRecordMetadataScope(t *testing.T, server *Server, resp map[string]interface{}, want string) {
	t.Helper()
	respID, _ := resp["id"].(string)
	if respID == "" {
		t.Fatalf("response missing id: %#v", resp)
	}
	rec, ok, err := server.responsesState.loadResponse(respID)
	if err != nil || !ok {
		t.Fatalf("loadResponse(%q) = ok:%v err:%v", respID, ok, err)
	}
	if got := rec.Metadata["scope"]; got != want {
		t.Fatalf("response metadata scope = %#v, want %q; record = %#v", got, want, rec)
	}
}

func requestJSONStatus(t *testing.T, server http.Handler, method, path string, payload map[string]interface{}) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal(payload) error = %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody
}

func requestJSON(t *testing.T, server http.Handler, method, path string, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal(payload) error = %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s status = %d, body = %s", method, path, resp.StatusCode, string(respBody))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v, body = %s", err, string(respBody))
	}
	return decoded
}

func waitForChannel(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

type blockingRequestBody struct {
	payload []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRequestBody(payload []byte) *blockingRequestBody {
	return &blockingRequestBody{
		payload: append([]byte(nil), payload...),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingRequestBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	if len(b.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.payload)
	b.payload = b.payload[n:]
	return n, nil
}

func waitForBackgroundIdle(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.backgroundMu.Lock()
		count := len(server.backgroundCancel)
		server.backgroundMu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	server.backgroundMu.Lock()
	count := len(server.backgroundCancel)
	server.backgroundMu.Unlock()
	t.Fatalf("background responses still running: %d", count)
}
