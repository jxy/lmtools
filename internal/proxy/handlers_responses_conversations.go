package proxy

import (
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"strings"
	"time"
)

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
	messages, err := conversationMessagesFromRequest(ctx, body)
	if err != nil {
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
	beforeMessages, err := session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, conv.SessionPath)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeServer, err.Error(), "state_error", http.StatusInternalServerError)
		return
	}
	beforeItemCount := len(coreResponsesInput(beforeMessages))
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
	appended := conversationItemsFromMessages(messages, beforeItemCount, conversationDeletedItemSet(&conv), false)
	_ = s.sendJSONResponse(ctx, w, listPayload(appended))
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
