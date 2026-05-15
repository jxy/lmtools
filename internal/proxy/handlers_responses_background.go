package proxy

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"lmtools/internal/logger"
	"net/http"
	"time"
)

func responsesBackgroundRequested(req *OpenAIResponsesRequest) bool {
	return req != nil && req.Background != nil && *req.Background
}

func (s *Server) handleConvertedOpenAIResponsesBackground(w http.ResponseWriter, r *http.Request, responsesReq *OpenAIResponsesRequest, typedCurrent TypedRequest, route *endpointRoute) {
	ctx := r.Context()
	stateCtx, typedWithState, err := s.prepareOpenAIResponsesStateWithMode(ctx, responsesReq, typedCurrent, route.OriginalModel, responsesStateBackground, responsesStoreRequested(responsesReq))
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

	if err := s.updateBackgroundResponseStatus(respID, "in_progress", nil); err != nil {
		logger.From(ctx).Errorf("failed to mark background response %s in_progress: %v", respID, err)
	}
	upstreamResp, err := s.forwardTypedAsAnthropic(ctx, typedWithState, route.Provider, route.MappedModel, route.OriginalModel)
	if err != nil {
		status := "failed"
		errPayload := map[string]interface{}{"code": "upstream_error", "message": err.Error()}
		if ctx.Err() != nil {
			status = "cancelled"
			errPayload = map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"}
		}
		if err := s.updateBackgroundResponseStatus(respID, status, errPayload); err != nil {
			logger.From(ctx).Errorf("failed to mark background response %s %s: %v", respID, status, err)
		}
		return
	}
	if ctx.Err() != nil {
		if err := s.updateBackgroundResponseStatus(respID, "cancelled", map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"}); err != nil {
			logger.From(ctx).Errorf("failed to mark background response %s cancelled: %v", respID, err)
		}
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
		if statusErr := s.updateBackgroundResponseStatus(respID, "failed", map[string]interface{}{"code": "state_error", "message": err.Error()}); statusErr != nil {
			logger.From(ctx).Errorf("failed to mark background response %s failed: %v", respID, statusErr)
		}
	}
}

func (s *Server) updateBackgroundResponseStatus(id, status string, errPayload interface{}) error {
	return s.responsesState.updateResponseStatusIfPending(id, status, errPayload)
}
