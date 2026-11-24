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

func (s *Server) handleConvertedOpenAIResponsesBackground(w http.ResponseWriter, r *http.Request, responsesReq *OpenAIResponsesRequest, rawRequest json.RawMessage, typedCurrent TypedRequest, route *endpointRoute) {
	ctx := r.Context()
	stateCtx, typedWithState, err := s.prepareOpenAIResponsesStateBackground(ctx, responsesReq, rawRequest, typedCurrent)
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
		Session:            stateCtx.Session,
		CreatedAt:          now,
		Background:         true,
		Store:              stateCtx.Store,
		Instructions:       stateCtx.Instructions,
		Metadata:           cloneStringInterfaceMap(responsesReq.Metadata),
		Request:            stateCtx.CurrentRequest,
		Raw:                initialRaw,
	}
	if stateCtx.Conversation != nil {
		rec.ConversationID = stateCtx.Conversation.ID
	}
	if err := s.responsesState.saveResponse(ctx, rec); err != nil {
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

// backgroundStartForTest runs as the worker of background response id
// starts, before it marks the response in progress.
var backgroundStartForTest func(id string)

func (s *Server) runConvertedOpenAIResponsesBackground(ctx context.Context, responsesReq *OpenAIResponsesRequest, typedCurrent, typedWithState TypedRequest, stateCtx *openAIResponsesStateContext, route *endpointRoute, respID string) {
	defer func() {
		s.backgroundMu.Lock()
		delete(s.backgroundCancel, respID)
		s.backgroundMu.Unlock()
	}()

	if backgroundStartForTest != nil {
		backgroundStartForTest(respID)
	}
	// A response deleted or cancelled before its worker starts, also through
	// another apiproxy, is not sent.
	if err := s.responsesState.updateResponseStatusIfPending(ctx, respID, "in_progress", nil); err != nil {
		if stdErrors.Is(err, errResponsesStateDeleted) || stdErrors.Is(err, errResponsesStateNotActive) {
			return
		}
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
		if err := s.responsesState.updateResponseStatusIfPending(ctx, respID, status, errPayload); err != nil {
			logger.From(ctx).Errorf("failed to mark background response %s %s: %v", respID, status, err)
		}
		return
	}
	if ctx.Err() != nil {
		if err := s.responsesState.updateResponseStatusIfPending(ctx, respID, "cancelled", map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"}); err != nil {
			logger.From(ctx).Errorf("failed to mark background response %s cancelled: %v", respID, err)
		}
		return
	}
	registry := responseToolNameRegistryFromCoreTools(typedWithState.Tools)
	resp := ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(upstreamResp, route.OriginalModel, registry)
	resp.ID = respID
	stateCtx.ExistingRecordID = respID
	// A response deleted or cancelled meanwhile keeps the record that says
	// so. Any other failure, its conversation's deletion included, ends the
	// response failed, or cancelled when its context ended.
	if err := s.commitOpenAIResponsesStateWithBlocks(ctx, stateCtx, responsesReq, typedCurrent, resp, route.OriginalModel, AnthropicBlocksToCoreWithToolNameRegistry(upstreamResp.Content, registry)); err != nil {
		if stdErrors.Is(err, errResponsesStateDeleted) || stdErrors.Is(err, errResponsesStateNotActive) {
			return
		}
		status, errPayload := "failed", map[string]interface{}{"code": "state_error", "message": err.Error()}
		if ctx.Err() != nil {
			status, errPayload = "cancelled", map[string]interface{}{"code": "cancelled", "message": "Response was cancelled"}
		}
		if statusErr := s.responsesState.updateResponseStatusIfPending(ctx, respID, status, errPayload); statusErr != nil {
			logger.From(ctx).Errorf("failed to mark background response %s %s: %v", respID, status, statusErr)
		}
	}
}
