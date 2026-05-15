package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"lmtools/internal/session"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type openAIResponsesStateContext struct {
	Session                           *session.Session
	Conversation                      *conversationRecord
	Previous                          *responseRecord
	History                           []core.TypedMessage
	Store                             bool
	Background                        bool
	Instructions                      string
	CurrentRequest                    json.RawMessage
	ExistingRecordID                  string
	ConversationBaseSessionPath       string
	ConversationBaseLastResponseID    string
	ConversationBaseMessagePath       string
	ConversationBaseTerminalMessageID string
}

type openAIResponsesStateMode int

const (
	responsesStateReadOnly openAIResponsesStateMode = iota
	responsesStateForeground
	responsesStateBackground
)

func (mode openAIResponsesStateMode) readOnly() bool {
	return mode == responsesStateReadOnly
}

func (mode openAIResponsesStateMode) background() bool {
	return mode == responsesStateBackground
}

func (mode openAIResponsesStateMode) writable(store bool) bool {
	return mode == responsesStateBackground || (mode == responsesStateForeground && store)
}

func (s *Server) handleOpenAIResponsesLifecycle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rest := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Response id is required", "", http.StatusBadRequest)
		return
	}
	id := parts[0]

	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.retrieveOpenAIResponse(ctx, w, r, id)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteOpenAIResponse(ctx, w, r, id)
	case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
		s.cancelOpenAIResponse(ctx, w, r, id)
	case len(parts) == 2 && parts[1] == "input_items" && r.Method == http.MethodGet:
		s.listOpenAIResponseInputItems(ctx, w, r, id)
	default:
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Unsupported Responses API lifecycle operation", "", http.StatusNotFound)
	}
}

func (s *Server) handleOpenAIResponsesInputTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	req, rawBody, route, ok := s.parseOpenAIResponsesUtilityRequest(w, r, "OpenAI responses input_tokens endpoint")
	if !ok {
		return
	}
	if route.Provider == constants.ProviderOpenAI {
		req.Model = route.MappedModel
		s.forwardOpenAIRawLifecycleWithBody(w, r, "responses", rewriteResponsesRequestModel(rawBody, route.MappedModel))
		return
	}
	req.Model = route.MappedModel
	typed, err := OpenAIResponsesRequestToTyped(ctx, req)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "conversion_error", http.StatusBadRequest)
		return
	}
	_, typedWithState, err := s.prepareOpenAIResponsesStateReadOnly(ctx, req, typed, route.OriginalModel)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "state_error", http.StatusBadRequest)
		return
	}

	inputTokens, err := s.countConvertedOpenAIResponsesInputTokens(ctx, typedWithState, route)
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, route.Provider, err)
		return
	}

	_ = s.sendJSONResponse(ctx, w, OpenAIResponsesInputTokensResponse{
		Object:      "response.input_tokens",
		InputTokens: inputTokens,
	})
}

func (s *Server) countConvertedOpenAIResponsesInputTokens(ctx context.Context, typed TypedRequest, route *endpointRoute) (int, error) {
	switch route.Provider {
	case constants.ProviderAnthropic:
		return s.countOpenAIResponsesInputTokensWithAnthropic(ctx, typed, route.MappedModel, constants.ProviderAnthropic)
	case constants.ProviderArgo:
		if !s.useLegacyArgo() && providers.DetermineArgoModelProvider(route.MappedModel) == constants.ProviderAnthropic {
			return s.countOpenAIResponsesInputTokensWithAnthropic(ctx, typed, route.MappedModel, constants.ProviderArgo)
		}
	case constants.ProviderGoogle:
		return s.countOpenAIResponsesInputTokensWithGoogle(ctx, typed, route.MappedModel)
	}
	return estimateOpenAIResponsesInputTokens(typed, route.MappedModel)
}

func (s *Server) countOpenAIResponsesInputTokensWithAnthropic(ctx context.Context, typed TypedRequest, model, provider string) (int, error) {
	anthReq, err := TypedToAnthropicRequest(typed, model)
	if err != nil {
		return 0, err
	}
	countReq := &AnthropicTokenCountRequest{
		Model:    anthReq.Model,
		System:   anthReq.System,
		Messages: anthReq.Messages,
		Tools:    anthReq.Tools,
	}
	var countResp *AnthropicTokenCountResponse
	if provider == constants.ProviderArgo {
		countResp, err = s.forwardArgoCountTokens(ctx, countReq)
	} else {
		countResp, err = s.forwardAnthropicCountTokens(ctx, countReq)
	}
	if err != nil {
		return 0, err
	}
	return countResp.InputTokens, nil
}

func (s *Server) countOpenAIResponsesInputTokensWithGoogle(ctx context.Context, typed TypedRequest, model string) (int, error) {
	googleReq, err := TypedToGoogleRequest(typed, model, nil)
	if err != nil {
		return 0, err
	}
	countResp, err := s.forwardGoogleCountTokens(ctx, googleReq, model)
	if err != nil {
		return 0, err
	}
	return countResp.TotalTokens, nil
}

func estimateOpenAIResponsesInputTokens(typed TypedRequest, model string) (int, error) {
	anthReq, err := TypedToAnthropicRequest(typed, model)
	if err != nil {
		return 0, err
	}
	return EstimateRequestTokens(anthReq), nil
}

func (s *Server) handleOpenAIResponsesCompact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	req, rawBody, route, ok := s.parseOpenAIResponsesUtilityRequest(w, r, "OpenAI responses compact endpoint")
	if !ok {
		return
	}
	if route.Provider == constants.ProviderOpenAI {
		req.Model = route.MappedModel
		s.forwardOpenAIRawLifecycleWithBody(w, r, "responses", rewriteResponsesRequestModel(rawBody, route.MappedModel))
		return
	}
	req.Model = route.MappedModel
	typed, err := OpenAIResponsesRequestToTyped(ctx, req)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "conversion_error", http.StatusBadRequest)
		return
	}
	stateCtx, typedWithState, err := s.prepareOpenAIResponsesStateReadOnly(ctx, req, typed, route.OriginalModel)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "state_error", http.StatusBadRequest)
		return
	}

	compactReq := typedWithState
	compactReq.Stream = false
	compactReq.Tools = nil
	compactReq.ToolChoice = nil
	compactReq.Developer = combineInstructionText(compactReq.Developer, "Compact the conversation state for future continuation. Preserve durable facts, decisions, unresolved tasks, tool calls and tool results, constraints, and user preferences. Return only the compacted state summary.")
	upstreamResp, err := s.forwardTypedAsAnthropic(ctx, compactReq, route.Provider, route.MappedModel, route.OriginalModel)
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, route.Provider, err)
		return
	}

	summary := strings.TrimSpace(anthropicResponseText(upstreamResp))
	if summary == "" {
		summary = "No compacted state was produced."
	}
	resp := convertedOpenAIResponsesCompaction(stateCtx, typedWithState, summary, upstreamResp)
	_ = s.sendJSONResponse(ctx, w, resp)
}

func (s *Server) parseOpenAIResponsesUtilityRequest(w http.ResponseWriter, r *http.Request, endpointName string) (*OpenAIResponsesRequest, []byte, *endpointRoute, bool) {
	var responsesReq *OpenAIResponsesRequest
	var rawBody []byte
	_, route, ok := s.handlePOSTEndpoint(
		w,
		r,
		endpointName,
		func(r *http.Request) (*endpointRequestInfo, error) {
			var req OpenAIResponsesRequest
			body, err := s.decodeEndpointRequestWithDisposition(r, &req, "preserved for direct OpenAI passthrough, ignored by converted providers")
			if err != nil {
				return nil, err
			}
			rawBody = body
			if req.Model == "" {
				req.Model = s.defaultOpenAIResponsesUtilityModel(&req)
			}
			if req.Model == "" {
				return nil, fmt.Errorf("model is required")
			}
			responsesReq = &req
			return &endpointRequestInfo{
				Model:     req.Model,
				Payload:   &req,
				ToolCount: len(req.Tools),
				Tools:     req.Tools,
			}, nil
		},
		endpointErrorHandlers{
			MethodNotAllowed: func() {
				s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
			},
			BadRequest: func(message string) {
				s.sendOpenAIError(w, ErrTypeInvalidRequest, message, "", http.StatusBadRequest)
			},
			ConfigError: func(message string) {
				s.sendOpenAIError(w, ErrTypeInvalidRequest, message, "configuration_error", http.StatusInternalServerError)
			},
			AuthError: func(message string) {
				s.sendOpenAIError(w, ErrTypeAuthentication, message, "unauthorized", http.StatusUnauthorized)
			},
		},
	)
	if !ok {
		return nil, nil, nil, false
	}
	return responsesReq, rawBody, route, true
}

func (s *Server) defaultOpenAIResponsesUtilityModel(req *OpenAIResponsesRequest) string {
	if req != nil && req.PreviousResponseID != "" {
		if rec, ok, err := s.responsesState.loadResponse(req.PreviousResponseID); err == nil && ok && rec.Model != "" {
			return rec.Model
		}
	}
	return ""
}

func (s *Server) loadCompletedPreviousResponse(id string) (*responseRecord, error) {
	prev, ok, err := s.responsesState.loadResponse(id)
	if err != nil {
		return nil, err
	}
	if !ok || prev.Deleted {
		return nil, fmt.Errorf("previous_response_id %q was not found", id)
	}
	if prev.Status != "completed" {
		return nil, fmt.Errorf("previous_response_id %q is not completed (status: %s)", id, firstNonEmpty(prev.Status, "unknown"))
	}
	return prev, nil
}

func (s *Server) retrieveOpenAIResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	if rec, ok, err := s.responsesState.loadResponse(id); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	} else if ok && !rec.Deleted {
		_ = s.sendJSONResponse(ctx, w, responseRecordPayload(rec))
		return
	}
	if s.config.Provider == constants.ProviderOpenAI {
		s.forwardOpenAIRawLifecycle(w, r, "responses")
		return
	}
	s.sendOpenAIError(w, ErrTypeInvalidRequest, "Response not found", "not_found", http.StatusNotFound)
}

func (s *Server) deleteOpenAIResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	if _, ok, err := s.responsesState.deleteResponse(id); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	} else if ok {
		s.cancelBackgroundResponse(id)
		_ = s.sendJSONResponse(ctx, w, map[string]interface{}{
			"id":      id,
			"object":  "response.deleted",
			"deleted": true,
		})
		return
	}
	if s.config.Provider == constants.ProviderOpenAI {
		s.forwardOpenAIRawLifecycle(w, r, "responses")
		return
	}
	s.sendOpenAIError(w, ErrTypeInvalidRequest, "Response not found", "not_found", http.StatusNotFound)
}

func (s *Server) cancelOpenAIResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	rec, ok, err := s.responsesState.loadResponse(id)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if !ok || rec.Deleted {
		if s.config.Provider == constants.ProviderOpenAI {
			s.forwardOpenAIRawLifecycle(w, r, "responses")
			return
		}
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Response not found", "not_found", http.StatusNotFound)
		return
	}

	s.cancelBackgroundResponse(id)

	if rec.Status == "queued" || rec.Status == "in_progress" {
		updated, ok, err := s.responsesState.cancelResponseIfPending(id, map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"})
		if err != nil {
			s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
			return
		}
		if ok && updated != nil {
			rec = updated
		}
	}
	_ = s.sendJSONResponse(ctx, w, responseRecordPayload(rec))
}

func (s *Server) cancelBackgroundResponse(id string) bool {
	s.backgroundMu.Lock()
	cancel := s.backgroundCancel[id]
	if cancel != nil {
		cancel()
	}
	s.backgroundMu.Unlock()
	return cancel != nil
}

func (s *Server) cancelBackgroundResponsesForConversation(conversationID string) {
	s.backgroundMu.Lock()
	ids := make([]string, 0, len(s.backgroundCancel))
	for id := range s.backgroundCancel {
		ids = append(ids, id)
	}
	s.backgroundMu.Unlock()

	for _, id := range ids {
		rec, ok, err := s.responsesState.loadResponse(id)
		if err != nil || !ok || rec.Deleted || rec.ConversationID != conversationID {
			continue
		}
		s.cancelBackgroundResponse(id)
	}
}

func (s *Server) listOpenAIResponseInputItems(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	rec, ok, err := s.responsesState.loadResponse(id)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if !ok || rec.Deleted {
		if s.config.Provider == constants.ProviderOpenAI {
			s.forwardOpenAIRawLifecycle(w, r, "responses")
			return
		}
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Response not found", "not_found", http.StatusNotFound)
		return
	}
	items := responseRecordInputItems(rec)
	payload, err := paginatedListPayload(items, r.URL.Query())
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	_ = s.sendJSONResponse(ctx, w, payload)
}

func (s *Server) handleOpenAIConversations(w http.ResponseWriter, r *http.Request) {
	if s.config.Provider == constants.ProviderOpenAI {
		s.forwardOpenAIRawLifecycle(w, r, "conversations")
		return
	}

	ctx := r.Context()
	rest := strings.TrimPrefix(r.URL.Path, "/v1/conversations")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		switch r.Method {
		case http.MethodPost:
			s.createOpenAIConversation(ctx, w, r)
		default:
			s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	parts := strings.Split(rest, "/")
	id := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.retrieveOpenAIConversation(ctx, w, id)
	case len(parts) == 1 && r.Method == http.MethodPost:
		s.updateOpenAIConversation(ctx, w, r, id)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteOpenAIConversation(ctx, w, id)
	case len(parts) == 2 && parts[1] == "items" && r.Method == http.MethodGet:
		s.listOpenAIConversationItems(ctx, w, r, id)
	case len(parts) == 2 && parts[1] == "items" && r.Method == http.MethodPost:
		s.appendOpenAIConversationItems(ctx, w, r, id)
	case len(parts) == 3 && parts[1] == "items" && r.Method == http.MethodGet:
		s.retrieveOpenAIConversationItem(ctx, w, id, parts[2])
	case len(parts) == 3 && parts[1] == "items" && r.Method == http.MethodDelete:
		s.deleteOpenAIConversationItem(ctx, w, id, parts[2])
	default:
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Unsupported Conversations API operation", "", http.StatusNotFound)
	}
}

func (s *Server) createOpenAIConversation(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var body openAIConversationRequestBody
	if err := s.decodeOptionalJSON(r, &body); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	instructions := responsesInstructionText(body.Instructions)
	messages, err := conversationMessagesFromRequest(ctx, body)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	conv, sess, err := s.responsesState.createConversation(body.Metadata, instructions)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	if err := appendConversationMessages(ctx, sess, messages); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	conv.SessionPath = sess.Path
	if err := s.responsesState.saveConversation(conv); err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	_ = s.sendJSONResponse(ctx, w, conversationPayload(conv))
}

func (s *Server) retrieveOpenAIConversation(ctx context.Context, w http.ResponseWriter, id string) {
	conv, ok, err := s.responsesState.loadConversation(id)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if !ok {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation not found", "not_found", http.StatusNotFound)
		return
	}
	_ = s.sendJSONResponse(ctx, w, conversationPayload(conv))
}

func (s *Server) updateOpenAIConversation(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	conv, ok, err := s.responsesState.loadConversation(id)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if !ok {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation not found", "not_found", http.StatusNotFound)
		return
	}
	var body openAIConversationRequestBody
	if err := s.decodeOptionalJSON(r, &body); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if body.Metadata != nil {
		conv.Metadata = cloneStringInterfaceMap(body.Metadata)
	}
	if instructions := responsesInstructionText(body.Instructions); instructions != "" {
		conv.Instructions = instructions
	}
	if err := s.responsesState.saveConversation(conv); err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	_ = s.sendJSONResponse(ctx, w, conversationPayload(conv))
}

func (s *Server) deleteOpenAIConversation(ctx context.Context, w http.ResponseWriter, id string) {
	if _, ok, err := s.responsesState.deleteConversation(id); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	} else if !ok {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation not found", "not_found", http.StatusNotFound)
		return
	}
	s.cancelBackgroundResponsesForConversation(id)
	_ = s.sendJSONResponse(ctx, w, map[string]interface{}{
		"id":      id,
		"object":  "conversation.deleted",
		"deleted": true,
	})
}

func (s *Server) listOpenAIConversationItems(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	items, err := s.conversationItems(ctx, id)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	payload, err := paginatedListPayload(items, r.URL.Query())
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	_ = s.sendJSONResponse(ctx, w, payload)
}

func (s *Server) appendOpenAIConversationItems(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	if _, ok, err := s.responsesState.loadConversation(id); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	} else if !ok {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation not found", "not_found", http.StatusNotFound)
		return
	}

	var body openAIConversationRequestBody
	if err := s.decodeOptionalJSON(r, &body); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}

	s.responsesState.mu.Lock()
	defer s.responsesState.mu.Unlock()

	var conv conversationRecord
	ok, err := s.responsesState.readJSON(s.responsesState.conversationPath(id), &conv)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if !ok || conv.Deleted {
		s.sendOpenAIError(w, ErrTypeServer, errResponsesStateDeleted.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	sess, err := s.responsesState.loadSession(conv.SessionPath)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	beforeItems, err := s.conversationItemsForRecord(ctx, &conv, true)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	messages, err := conversationMessagesFromRequest(ctx, body)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if err := appendConversationMessages(ctx, sess, messages); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	conv.SessionPath = sess.Path
	conv.UpdatedAt = time.Now().Unix()
	if err := s.responsesState.saveConversationLocked(&conv); err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	items, err := s.conversationItemsForRecord(ctx, &conv, true)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	appended := items[len(beforeItems):]
	_ = s.sendJSONResponse(ctx, w, listPayload(filterDeletedConversationItems(&conv, appended)))
}

func (s *Server) retrieveOpenAIConversationItem(ctx context.Context, w http.ResponseWriter, conversationID, itemID string) {
	items, err := s.conversationItems(ctx, conversationID)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	for _, item := range items {
		if itemMap, ok := item.(map[string]interface{}); ok && itemMap["id"] == itemID {
			_ = s.sendJSONResponse(ctx, w, itemMap)
			return
		}
	}
	s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation item not found", "not_found", http.StatusNotFound)
}

func (s *Server) deleteOpenAIConversationItem(ctx context.Context, w http.ResponseWriter, conversationID, itemID string) {
	conv, ok, err := s.responsesState.loadConversation(conversationID)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	if !ok {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation not found", "not_found", http.StatusNotFound)
		return
	}
	items, err := s.conversationItemsForRecord(ctx, conv, false)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	for _, item := range items {
		if itemMap, ok := item.(map[string]interface{}); ok && itemMap["id"] == itemID {
			conv.DeletedItemIDs = appendUniqueString(conv.DeletedItemIDs, itemID)
			if err := s.responsesState.saveConversation(conv); err != nil {
				s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
				return
			}
			_ = s.sendJSONResponse(ctx, w, conversationPayload(conv))
			return
		}
	}
	s.sendOpenAIError(w, ErrTypeInvalidRequest, "Conversation item not found", "not_found", http.StatusNotFound)
}

func (s *Server) prepareOpenAIResponsesState(ctx context.Context, req *OpenAIResponsesRequest, typed TypedRequest, originalModel string, background bool) (*openAIResponsesStateContext, TypedRequest, error) {
	store := req.Store == nil || *req.Store
	mode := responsesStateForeground
	if background {
		mode = responsesStateBackground
	}
	return s.prepareOpenAIResponsesStateWithMode(ctx, req, typed, originalModel, mode, store)
}

func (s *Server) prepareOpenAIResponsesStateReadOnly(ctx context.Context, req *OpenAIResponsesRequest, typed TypedRequest, originalModel string) (*openAIResponsesStateContext, TypedRequest, error) {
	return s.prepareOpenAIResponsesStateWithMode(ctx, req, typed, originalModel, responsesStateReadOnly, false)
}

func (s *Server) prepareOpenAIResponsesStateWithMode(ctx context.Context, req *OpenAIResponsesRequest, typed TypedRequest, originalModel string, mode openAIResponsesStateMode, store bool) (*openAIResponsesStateContext, TypedRequest, error) {
	convSpec, err := parseConversationSpec(req.Conversation)
	if err != nil {
		return nil, typed, err
	}
	if mode.readOnly() && convSpec.create {
		return nil, typed, fmt.Errorf("conversation id is required")
	}
	writable := mode.writable(store)
	if !mode.readOnly() {
		needsState := writable || req.PreviousResponseID != "" || (convSpec.requested && !convSpec.create)
		if !needsState {
			return nil, typed, nil
		}
	}

	currentRequest, err := marshalJSONRaw(req)
	if err != nil {
		return nil, typed, err
	}
	stateCtx := &openAIResponsesStateContext{
		Store:          store,
		Background:     mode.background(),
		Instructions:   responsesInstructionText(req.Instructions),
		CurrentRequest: currentRequest,
	}
	if stateCtx.Instructions == "" {
		stateCtx.Instructions = typed.Developer
	}

	var sessionPath string
	if req.PreviousResponseID != "" {
		prev, err := s.loadCompletedPreviousResponse(req.PreviousResponseID)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Previous = prev
		sessionPath = prev.SessionPath
		if stateCtx.Instructions == "" {
			stateCtx.Instructions = prev.Instructions
		}
	}

	if convSpec.requested && (mode.readOnly() || !convSpec.create || writable) {
		conv, sess, err := s.prepareOpenAIResponsesConversationState(stateCtx, convSpec, writable)
		if err != nil {
			return nil, typed, err
		}
		if stateCtx.Previous == nil {
			sessionPath = conv.SessionPath
			stateCtx.Session = sess
		}
		stateCtx.Conversation = conv
		if !mode.readOnly() {
			if err := s.captureOpenAIResponsesConversationBase(stateCtx, conv); err != nil {
				return nil, typed, err
			}
		}
		if stateCtx.Instructions == "" {
			stateCtx.Instructions = conv.Instructions
		}
	}

	if stateCtx.Previous != nil && writable {
		forkedSession, err := s.forkOpenAIResponsesResponseSnapshot(ctx, stateCtx.Previous)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Session = forkedSession
		sessionPath = forkedSession.Path
	}

	if sessionPath == "" && writable {
		sess, err := s.responsesState.createSession()
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Session = sess
	} else if sessionPath != "" && stateCtx.Session == nil {
		sess, err := s.responsesState.loadSession(sessionPath)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Session = sess
	}

	if stateCtx.Session != nil {
		typed, err = s.prependOpenAIResponsesStateHistory(ctx, stateCtx, typed, mode, writable)
		if err != nil {
			return nil, typed, err
		}
	}
	if typed.Developer == "" {
		typed.Developer = stateCtx.Instructions
	}
	if originalModel != "" && typed.Metadata == nil {
		typed.Metadata = map[string]interface{}{}
	}
	return stateCtx, typed, nil
}

func (s *Server) prepareOpenAIResponsesConversationState(stateCtx *openAIResponsesStateContext, convSpec conversationSpec, writable bool) (*conversationRecord, *session.Session, error) {
	var conv *conversationRecord
	var sess *session.Session
	var err error
	if convSpec.create {
		if stateCtx.Previous != nil {
			return nil, nil, fmt.Errorf("conversation does not match previous_response_id")
		}
		conv, sess, err = s.responsesState.createConversation(convSpec.metadata, stateCtx.Instructions)
		if err != nil {
			return nil, nil, err
		}
	} else {
		var ok bool
		conv, ok, err = s.responsesState.loadConversation(convSpec.id)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, fmt.Errorf("conversation %q was not found", convSpec.id)
		}
		if writable && len(convSpec.metadata) > 0 {
			conv.Metadata = cloneStringInterfaceMap(convSpec.metadata)
		}
	}
	if stateCtx.Previous != nil && stateCtx.Previous.ConversationID != conv.ID {
		return nil, nil, fmt.Errorf("conversation does not match previous_response_id")
	}
	return conv, sess, nil
}

func (s *Server) prependOpenAIResponsesStateHistory(ctx context.Context, stateCtx *openAIResponsesStateContext, typed TypedRequest, mode openAIResponsesStateMode, writable bool) (TypedRequest, error) {
	var history []core.TypedMessage
	var err error
	if stateCtx.Previous != nil && (!writable || mode.readOnly()) {
		history, err = s.responseRecordHistory(ctx, stateCtx.Previous)
	} else if stateCtx.Conversation != nil && stateCtx.Previous == nil {
		history, err = s.conversationHistory(ctx, stateCtx.Conversation)
	} else {
		history, err = session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, stateCtx.Session.Path)
	}
	if err != nil {
		return typed, err
	}
	stateCtx.History = history
	typed.Messages = append(append([]core.TypedMessage{}, history...), typed.Messages...)
	return typed, nil
}

func convertedOpenAIResponsesCompaction(stateCtx *openAIResponsesStateContext, typedWithState TypedRequest, summary string, upstreamResp *AnthropicResponse) *OpenAIResponsesCompactionResponse {
	now := time.Now().Unix()
	return &OpenAIResponsesCompactionResponse{
		ID:        generateUUID("resp_"),
		Object:    "response.compaction",
		CreatedAt: now,
		Output:    compactedResponseOutputItems(stateCtx, typedWithState.Messages, summary),
		Usage:     anthropicUsageToResponsesUsage(upstreamResp),
	}
}

type openAIResponsesCommitTarget struct {
	Session             *session.Session
	Conversation        *conversationRecord
	AdvanceConversation bool
	ExistingCreatedAt   int64
}

func (s *Server) commitOpenAIResponsesStateWithBlocks(ctx context.Context, stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, typedCurrent TypedRequest, resp *OpenAIResponsesResponse, originalModel string, assistantBlocks []core.Block) error {
	if resp == nil {
		return nil
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		attachOpenAIResponsesConversation(resp, stateCtx.Conversation.ID)
	}
	if stateCtx == nil || stateCtx.Session == nil {
		return nil
	}
	if !stateCtx.Store && !stateCtx.Background {
		return nil
	}
	if req == nil {
		req = &OpenAIResponsesRequest{Model: originalModel}
	}
	if stateCtx.ExistingRecordID != "" {
		resp.ID = stateCtx.ExistingRecordID
	}

	s.responsesState.mu.Lock()
	defer s.responsesState.mu.Unlock()

	target, err := s.prepareOpenAIResponsesCommitTargetLocked(ctx, stateCtx, resp.ID)
	if err != nil {
		return err
	}
	if target.ExistingCreatedAt != 0 {
		resp.CreatedAt = target.ExistingCreatedAt
	}
	raw, err := marshalJSONRaw(resp)
	if err != nil {
		return err
	}

	result, err := appendOpenAIResponsesCommitMessages(ctx, target.Session, typedCurrent, resp, originalModel, assistantBlocks)
	if err != nil {
		return err
	}
	target.Session.Path = result.Path
	stateCtx.Session = target.Session

	return s.saveOpenAIResponsesCommitRecordsLocked(stateCtx, req, resp, originalModel, result, target, raw)
}

func (s *Server) prepareOpenAIResponsesCommitTargetLocked(ctx context.Context, stateCtx *openAIResponsesStateContext, responseID string) (openAIResponsesCommitTarget, error) {
	if err := s.ensureOpenAIResponsesStateWritableLocked(stateCtx, responseID, true); err != nil {
		return openAIResponsesCommitTarget{}, err
	}
	target := openAIResponsesCommitTarget{Session: stateCtx.Session}
	existingCreatedAt := int64(0)
	if stateCtx.ExistingRecordID != "" {
		var existing responseRecord
		ok, err := s.responsesState.readJSON(s.responsesState.responsePath(stateCtx.ExistingRecordID), &existing)
		if err != nil {
			return openAIResponsesCommitTarget{}, err
		}
		if ok {
			existingCreatedAt = existing.CreatedAt
		}
	}
	target.ExistingCreatedAt = existingCreatedAt
	if stateCtx.Conversation != nil {
		conv, matches, err := s.openAIResponsesConversationHeadMatchesLocked(stateCtx)
		if err != nil {
			return openAIResponsesCommitTarget{}, err
		}
		target.Conversation = conv
		if matches {
			target.AdvanceConversation = true
		} else {
			forkedSession, err := s.forkOpenAIResponsesConversationBase(ctx, stateCtx)
			if err != nil {
				return openAIResponsesCommitTarget{}, err
			}
			target.Session = forkedSession
		}
	}
	return target, nil
}

func appendOpenAIResponsesCommitMessages(ctx context.Context, commitSession *session.Session, typedCurrent TypedRequest, resp *OpenAIResponsesResponse, originalModel string, assistantBlocks []core.Block) (session.SaveResult, error) {
	for _, msg := range typedCurrent.Messages {
		if err := appendTypedMessageToSession(ctx, commitSession, msg, originalModel); err != nil {
			return session.SaveResult{}, err
		}
	}
	assistantResponse := openAIResponsesCoreResponse(resp)
	if len(assistantBlocks) > 0 {
		assistantResponse.Blocks = assistantBlocks
	}
	result, err := session.SaveAssistantResponse(ctx, commitSession, assistantResponse, originalModel)
	if err != nil {
		return session.SaveResult{}, err
	}
	return result, nil
}

func (s *Server) saveOpenAIResponsesCommitRecordsLocked(stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, resp *OpenAIResponsesResponse, originalModel string, result session.SaveResult, target openAIResponsesCommitTarget, raw json.RawMessage) error {
	rec := &responseRecord{
		Version:            responsesStateVersion,
		ID:                 resp.ID,
		Object:             "response",
		Status:             firstNonEmpty(resp.Status, "completed"),
		Model:              originalModel,
		PreviousResponseID: req.PreviousResponseID,
		SessionPath:        stateCtx.Session.Path,
		MessageID:          result.MessageID,
		CreatedAt:          resp.CreatedAt,
		CompletedAt:        time.Now().Unix(),
		Background:         stateCtx.Background,
		Stream:             req.Stream,
		Store:              stateCtx.Store,
		Instructions:       stateCtx.Instructions,
		Metadata:           cloneStringInterfaceMap(req.Metadata),
		Request:            append(json.RawMessage(nil), stateCtx.CurrentRequest...),
		Raw:                raw,
		Error:              resp.Error,
		IncompleteDetails:  resp.IncompleteDetails,
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = rec.CompletedAt
	}
	if target.Conversation != nil {
		rec.ConversationID = target.Conversation.ID
		if target.AdvanceConversation {
			target.Conversation.SessionPath = target.Session.Path
			target.Conversation.LastResponseID = resp.ID
			if stateCtx.Conversation.Metadata != nil {
				target.Conversation.Metadata = cloneStringInterfaceMap(stateCtx.Conversation.Metadata)
			}
			if stateCtx.Instructions != "" {
				target.Conversation.Instructions = stateCtx.Instructions
			}
			if err := s.responsesState.saveConversationLocked(target.Conversation); err != nil {
				return err
			}
			stateCtx.Conversation = target.Conversation
		}
	}
	return s.responsesState.saveResponseLocked(rec)
}

func (s *Server) responseRecordHistory(ctx context.Context, rec *responseRecord) ([]core.TypedMessage, error) {
	if rec == nil {
		return nil, fmt.Errorf("response record is nil")
	}
	if rec.SessionPath == "" {
		return nil, fmt.Errorf("response session path is empty")
	}
	if rec.MessageID == "" {
		return session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, rec.SessionPath)
	}
	return session.BuildMessagesWithToolInteractionsThroughMessageWithManager(ctx, s.responsesState.manager, rec.SessionPath, rec.SessionPath, rec.MessageID)
}

func (s *Server) forkOpenAIResponsesResponseSnapshot(ctx context.Context, rec *responseRecord) (*session.Session, error) {
	if rec == nil {
		return nil, fmt.Errorf("response record is nil")
	}
	if rec.SessionPath == "" {
		return nil, fmt.Errorf("response session path is empty")
	}
	if rec.MessageID == "" {
		return session.ForkSessionWithManager(ctx, s.responsesState.manager, rec.SessionPath, nil)
	}
	return session.ForkSessionThroughMessageWithManager(ctx, s.responsesState.manager, rec.SessionPath, rec.SessionPath, rec.MessageID, nil)
}

func (s *Server) captureOpenAIResponsesConversationBase(stateCtx *openAIResponsesStateContext, conv *conversationRecord) error {
	if stateCtx == nil || conv == nil {
		return nil
	}
	if stateCtx.Previous != nil {
		stateCtx.ConversationBaseSessionPath = stateCtx.Previous.SessionPath
		stateCtx.ConversationBaseLastResponseID = stateCtx.Previous.ID
		stateCtx.ConversationBaseMessagePath = stateCtx.Previous.SessionPath
		stateCtx.ConversationBaseTerminalMessageID = stateCtx.Previous.MessageID
		if stateCtx.ConversationBaseTerminalMessageID == "" {
			ref, ok, err := session.LastMessageRefWithManager(s.responsesState.manager, stateCtx.Previous.SessionPath)
			if err != nil {
				return err
			}
			if ok {
				stateCtx.ConversationBaseMessagePath = ref.Path
				stateCtx.ConversationBaseTerminalMessageID = ref.ID
			}
		}
		return nil
	}

	stateCtx.ConversationBaseSessionPath = conv.SessionPath
	stateCtx.ConversationBaseLastResponseID = conv.LastResponseID
	ref, ok, err := session.LastMessageRefWithManager(s.responsesState.manager, conv.SessionPath)
	if err != nil {
		return err
	}
	if ok {
		stateCtx.ConversationBaseMessagePath = ref.Path
		stateCtx.ConversationBaseTerminalMessageID = ref.ID
	}
	return nil
}

func (s *Server) openAIResponsesConversationHeadMatchesLocked(stateCtx *openAIResponsesStateContext) (*conversationRecord, bool, error) {
	if stateCtx == nil || stateCtx.Conversation == nil {
		return nil, false, fmt.Errorf("conversation state is missing")
	}
	var conv conversationRecord
	ok, err := s.responsesState.readJSON(s.responsesState.conversationPath(stateCtx.Conversation.ID), &conv)
	if err != nil {
		return nil, false, err
	}
	if !ok || conv.Deleted {
		return nil, false, errResponsesStateDeleted
	}
	if conv.SessionPath != stateCtx.ConversationBaseSessionPath || conv.LastResponseID != stateCtx.ConversationBaseLastResponseID {
		return &conv, false, nil
	}

	ref, ok, err := session.LastMessageRefWithManager(s.responsesState.manager, conv.SessionPath)
	if err != nil {
		return nil, false, err
	}
	if stateCtx.ConversationBaseTerminalMessageID == "" {
		return &conv, !ok, nil
	}
	return &conv, ok && ref.Path == stateCtx.ConversationBaseMessagePath && ref.ID == stateCtx.ConversationBaseTerminalMessageID, nil
}

func (s *Server) forkOpenAIResponsesConversationBase(ctx context.Context, stateCtx *openAIResponsesStateContext) (*session.Session, error) {
	if stateCtx != nil && stateCtx.Previous != nil && stateCtx.Session != nil {
		return stateCtx.Session, nil
	}
	if stateCtx == nil || stateCtx.ConversationBaseSessionPath == "" {
		return nil, fmt.Errorf("conversation base session path is empty")
	}
	if stateCtx.ConversationBaseTerminalMessageID == "" {
		return s.responsesState.createSession()
	}
	return session.ForkSessionThroughMessageWithManager(
		ctx,
		s.responsesState.manager,
		stateCtx.ConversationBaseSessionPath,
		stateCtx.ConversationBaseMessagePath,
		stateCtx.ConversationBaseTerminalMessageID,
		nil,
	)
}

func (s *Server) ensureOpenAIResponsesStateWritableLocked(stateCtx *openAIResponsesStateContext, responseID string, requireActive bool) error {
	if responseID != "" {
		var rec responseRecord
		ok, err := s.responsesState.readJSON(s.responsesState.responsePath(responseID), &rec)
		if err != nil {
			return err
		}
		if ok && rec.Deleted {
			return errResponsesStateDeleted
		}
		if ok && requireActive && !responseRecordPending(rec.Status) {
			return errResponsesStateNotActive
		}
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		var conv conversationRecord
		ok, err := s.responsesState.readJSON(s.responsesState.conversationPath(stateCtx.Conversation.ID), &conv)
		if err != nil {
			return err
		}
		if !ok || conv.Deleted {
			return errResponsesStateDeleted
		}
	}
	return nil
}

func responsesBackgroundRequested(req *OpenAIResponsesRequest) bool {
	return req != nil && req.Background != nil && *req.Background
}

func (s *Server) handleConvertedOpenAIResponsesBackground(w http.ResponseWriter, r *http.Request, responsesReq *OpenAIResponsesRequest, typedCurrent TypedRequest, route *endpointRoute) {
	ctx := r.Context()
	stateCtx, typedWithState, err := s.prepareOpenAIResponsesState(ctx, responsesReq, typedCurrent, route.OriginalModel, true)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "state_error", http.StatusBadRequest)
		return
	}
	if stateCtx == nil {
		s.sendOpenAIError(w, ErrTypeServer, "Failed to allocate response state", "state_error", http.StatusInternalServerError)
		return
	}

	respID := generateUUID("resp_")
	stateCtx.ExistingRecordID = respID
	now := time.Now().Unix()
	initial := &OpenAIResponsesResponse{
		ID:        respID,
		Object:    "response",
		CreatedAt: now,
		Status:    "queued",
		Model:     route.OriginalModel,
		Output:    []OpenAIResponsesOutputItem{},
	}
	if stateCtx.Conversation != nil {
		attachOpenAIResponsesConversation(initial, stateCtx.Conversation.ID)
	}
	initialRaw, err := marshalJSONRaw(initial)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, "Failed to save response state", "state_error", http.StatusInternalServerError)
		return
	}
	rec := &responseRecord{
		Version:            responsesStateVersion,
		ID:                 respID,
		Object:             "response",
		Status:             "queued",
		Model:              route.OriginalModel,
		PreviousResponseID: responsesReq.PreviousResponseID,
		SessionPath:        stateCtx.Session.Path,
		CreatedAt:          now,
		Background:         true,
		Store:              stateCtx.Store,
		Instructions:       stateCtx.Instructions,
		Metadata:           cloneStringInterfaceMap(responsesReq.Metadata),
		Request:            append(json.RawMessage(nil), stateCtx.CurrentRequest...),
		Raw:                initialRaw,
	}
	if stateCtx.Conversation != nil {
		rec.ConversationID = stateCtx.Conversation.ID
	}
	if err := s.responsesState.saveResponse(rec); err != nil {
		s.sendOpenAIError(w, ErrTypeServer, "Failed to save response state", "state_error", http.StatusInternalServerError)
		return
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	s.backgroundMu.Lock()
	s.backgroundCancel[respID] = cancel
	s.backgroundMu.Unlock()

	go s.runConvertedOpenAIResponsesBackground(bgCtx, responsesReq, typedCurrent, typedWithState, stateCtx, route, respID)
	_ = s.sendJSONResponse(ctx, w, initial)
}

func (s *Server) runConvertedOpenAIResponsesBackground(ctx context.Context, responsesReq *OpenAIResponsesRequest, typedCurrent, typedWithState TypedRequest, stateCtx *openAIResponsesStateContext, route *endpointRoute, respID string) {
	defer func() {
		s.backgroundMu.Lock()
		delete(s.backgroundCancel, respID)
		s.backgroundMu.Unlock()
	}()

	s.updateBackgroundResponseStatus(respID, "in_progress", nil)
	upstreamResp, err := s.forwardTypedAsAnthropic(ctx, typedWithState, route.Provider, route.MappedModel, route.OriginalModel)
	if err != nil {
		status := "failed"
		errPayload := map[string]interface{}{"code": "upstream_error", "message": err.Error()}
		if ctx.Err() != nil {
			status = "cancelled"
			errPayload = map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"}
		}
		s.updateBackgroundResponseStatus(respID, status, errPayload)
		return
	}
	if ctx.Err() != nil {
		s.updateBackgroundResponseStatus(respID, "cancelled", map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"})
		return
	}
	registry := responseToolNameRegistryFromCoreTools(typedWithState.Tools)
	resp := s.converter.ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(upstreamResp, route.OriginalModel, registry)
	resp.ID = respID
	stateCtx.ExistingRecordID = respID
	if err := s.commitOpenAIResponsesStateWithBlocks(ctx, stateCtx, responsesReq, typedCurrent, resp, route.OriginalModel, AnthropicBlocksToCoreWithToolNameRegistry(upstreamResp.Content, registry)); err != nil {
		if stdErrors.Is(err, errResponsesStateDeleted) || stdErrors.Is(err, errResponsesStateNotActive) {
			return
		}
		s.updateBackgroundResponseStatus(respID, "failed", map[string]interface{}{"code": "state_error", "message": err.Error()})
	}
}

func (s *Server) updateBackgroundResponseStatus(id, status string, errPayload interface{}) {
	_ = s.responsesState.updateResponseStatusIfPending(id, status, errPayload)
}

func appendTypedMessageToSession(ctx context.Context, sess *session.Session, msg core.TypedMessage, model string) error {
	text, calls, results := typedMessageToolProjection(msg)
	role := core.Role(msg.Role)
	if role == "" {
		role = core.RoleUser
	}
	message := session.Message{
		Role:      role,
		Content:   text,
		Timestamp: time.Now(),
	}
	if role == core.RoleAssistant {
		message.Model = model
	}
	result, err := session.AppendMessageWithBlocks(ctx, sess, message, calls, results, msg.Blocks)
	if err != nil {
		return err
	}
	sess.Path = result.Path
	return nil
}

func (s *Server) forwardOpenAIRawLifecycle(w http.ResponseWriter, r *http.Request, family string) {
	ctx := r.Context()
	body, err := s.readRequestBody(r)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, fmt.Sprintf("failed to read request body: %v", err), "read_error", http.StatusBadRequest)
		return
	}
	logWireHTTPRequest(ctx, "WIRE CLIENT REQUEST", r, body)
	s.forwardOpenAIRawLifecycleWithBody(w, r, family, body)
}

func (s *Server) forwardOpenAIRawLifecycleWithBody(w http.ResponseWriter, r *http.Request, family string, body []byte) {
	ctx := r.Context()
	if s.endpoints.OpenAIResponses == "" {
		s.sendOpenAIError(w, ErrTypeServer, "OpenAI responses URL not configured", "configuration_error", http.StatusInternalServerError)
		return
	}
	target := s.openAIRawLifecycleURL(r, family)
	upstreamReq, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, "Failed to create upstream request", "upstream_error", http.StatusBadGateway)
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		upstreamReq.Header.Set("Content-Type", contentType)
	} else if len(body) > 0 {
		upstreamReq.Header.Set("Content-Type", "application/json")
	}
	if err := auth.ApplyProviderCredentials(upstreamReq, constants.ProviderOpenAI, s.config.OpenAIAPIKey); err != nil {
		s.sendOpenAIError(w, ErrTypeAuthentication, err.Error(), "unauthorized", http.StatusUnauthorized)
		return
	}
	logWireHTTPRequest(ctx, "WIRE BACKEND REQUEST OpenAI lifecycle", upstreamReq, body)
	resp, err := s.client.Do(ctx, upstreamReq, constants.ProviderOpenAI)
	if err != nil {
		logger.From(ctx).Errorf("OpenAI lifecycle request failed: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	logWireHTTPResponseHeaders(ctx, "WIRE BACKEND RESPONSE HEADERS OpenAI lifecycle", resp)
	respBody, err := s.readResponseBody(resp)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, "Failed to read upstream response", "read_error", http.StatusBadGateway)
		return
	}
	if family == "responses" && resp.StatusCode < http.StatusBadRequest {
		respBody = s.rewriteResponsesLifecycleBodyModel(respBody, responseIDFromResponsesLifecyclePath(r.URL.Path))
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	logWireBytes(ctx, "WIRE CLIENT RESPONSE BODY", respBody)
	_, _ = w.Write(respBody)
}

func (s *Server) rewriteResponsesLifecycleBodyModel(body []byte, fallbackResponseID string) []byte {
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return body
	}
	changed := false
	changed = s.rewriteResponsesObjectModel(decoded, fallbackResponseID) || changed
	if response, ok := decoded["response"].(map[string]interface{}); ok {
		changed = s.rewriteResponsesObjectModel(response, fallbackResponseID) || changed
	}
	if !changed {
		return body
	}
	updated, err := json.Marshal(decoded)
	if err != nil {
		return body
	}
	return updated
}

func (s *Server) rewriteResponsesObjectModel(response map[string]interface{}, fallbackResponseID string) bool {
	model, ok := response["model"].(string)
	if !ok {
		return false
	}
	responseID, _ := response["id"].(string)
	if responseID == "" {
		responseID = fallbackResponseID
	}
	visibleModel := s.clientVisibleResponsesModel(responseID, model)
	if visibleModel == model {
		return false
	}
	response["model"] = visibleModel
	return true
}

func responseIDFromResponsesLifecyclePath(path string) string {
	rest := strings.TrimPrefix(path, "/v1/responses/")
	if rest == path || rest == "" {
		return ""
	}
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return ""
	}
	return strings.Split(rest, "/")[0]
}

func (s *Server) openAIRawLifecycleURL(r *http.Request, family string) string {
	base := strings.TrimRight(s.endpoints.OpenAIResponses, "/")
	if family == "conversations" {
		base = strings.TrimSuffix(base, "/responses") + "/conversations"
		rest := strings.TrimPrefix(r.URL.Path, "/v1/conversations")
		if rest != "" {
			base += rest
		}
	} else {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/responses")
		if rest != "" {
			base += rest
		}
	}
	if r.URL.RawQuery != "" {
		base += "?" + r.URL.RawQuery
	}
	return base
}

func (s *Server) decodeOptionalJSON(r *http.Request, dst interface{}) error {
	body, err := s.readRequestBody(r)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	logWireHTTPRequest(r.Context(), "WIRE CLIENT REQUEST", r, body)
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid JSON in request body: %w", err)
	}
	return nil
}

func parseConversationSpec(raw interface{}) (conversationSpec, error) {
	if raw == nil {
		return conversationSpec{}, nil
	}
	switch value := raw.(type) {
	case string:
		switch strings.TrimSpace(value) {
		case "", "none":
			return conversationSpec{}, nil
		case "auto":
			return conversationSpec{requested: true, create: true}, nil
		default:
			return conversationSpec{requested: true, id: value}, nil
		}
	case map[string]interface{}:
		spec := conversationSpec{requested: true}
		if id, _ := value["id"].(string); id != "" {
			spec.id = id
		} else {
			spec.create = true
		}
		if metadata, _ := value["metadata"].(map[string]interface{}); metadata != nil {
			spec.metadata = metadata
		}
		return spec, nil
	default:
		return conversationSpec{}, fmt.Errorf("conversation must be a string or object")
	}
}

type conversationSpec struct {
	requested bool
	create    bool
	id        string
	metadata  map[string]interface{}
}

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
	items := coreResponsesInput(messages)
	out := make([]interface{}, 0, len(items))
	deleted := conversationDeletedItemSet(conv)
	for i, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		cloned := cloneMapInterface(itemMap)
		if _, exists := itemMap["id"]; !exists {
			cloned["id"] = conversationItemID(i)
		}
		if !includeDeleted && deleted[fmt.Sprint(cloned["id"])] {
			continue
		}
		out = append(out, cloned)
	}
	return out, nil
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

func filterDeletedConversationItems(conv *conversationRecord, items []interface{}) []interface{} {
	deleted := conversationDeletedItemSet(conv)
	if len(deleted) == 0 {
		return items
	}
	out := make([]interface{}, 0, len(items))
	for _, item := range items {
		if itemMap, ok := item.(map[string]interface{}); ok && deleted[fmt.Sprint(itemMap["id"])] {
			continue
		}
		out = append(out, item)
	}
	return out
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

type openAIConversationRequestBody struct {
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Instructions interface{}            `json:"instructions,omitempty"`
	Input        interface{}            `json:"input,omitempty"`
	Items        interface{}            `json:"items,omitempty"`
}

func conversationMessagesFromRequest(ctx context.Context, body openAIConversationRequestBody) ([]core.TypedMessage, error) {
	input := body.Items
	if input == nil {
		input = body.Input
	}
	if input == nil {
		return nil, nil
	}
	req := &OpenAIResponsesRequest{Model: "conversation-state", Input: input}
	typed, err := OpenAIResponsesRequestToTyped(ctx, req)
	if err != nil {
		return nil, err
	}
	return typed.Messages, nil
}

func appendConversationMessages(ctx context.Context, sess *session.Session, messages []core.TypedMessage) error {
	for _, msg := range messages {
		if err := appendTypedMessageToSession(ctx, sess, msg, ""); err != nil {
			return err
		}
	}
	return nil
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
