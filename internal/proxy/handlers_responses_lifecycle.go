package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"net/http"
	"strings"
)

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
	ctx := r.Context()
	log := logger.From(ctx)
	log.Infof("%s %s | %s", r.Method, r.URL.Path, endpointName)

	if r.Method != http.MethodPost {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		return nil, nil, nil, false
	}

	var req OpenAIResponsesRequest
	rawBody, err := s.decodeEndpointRequestWithDisposition(r, &req, "preserved for direct OpenAI passthrough, ignored by converted providers")
	if err != nil {
		log.Errorf("Failed to parse request: %s", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return nil, nil, nil, false
	}
	if req.Model == "" {
		req.Model = s.defaultOpenAIResponsesUtilityModel(&req)
	}
	if req.Model == "" {
		log.Errorf("Failed to parse request: model is required")
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "model is required", "", http.StatusBadRequest)
		return nil, nil, nil, false
	}
	info := endpointRequestInfo{
		Model:     req.Model,
		Payload:   &req,
		ToolCount: len(req.Tools),
		Tools:     req.Tools,
	}
	logEndpointRequest(ctx, info)

	route, routeErr := s.resolveEndpointRoute(ctx, info.Model)
	if routeErr != nil {
		if routeErr.Kind == endpointRouteAuthError {
			s.sendOpenAIError(w, ErrTypeAuthentication, routeErr.Message, "unauthorized", http.StatusUnauthorized)
			return nil, nil, nil, false
		}
		s.sendOpenAIError(w, ErrTypeInvalidRequest, routeErr.Message, "configuration_error", http.StatusInternalServerError)
		return nil, nil, nil, false
	}
	return &req, rawBody, route, true
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
