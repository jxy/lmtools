package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

func (s *Server) parseOpenAIResponsesRequest(r *http.Request) (*OpenAIResponsesRequest, []byte, error) {
	var req OpenAIResponsesRequest
	body, err := s.decodeEndpointRequestWithDisposition(r, &req, "preserved for direct OpenAI passthrough, ignored by converted providers")
	if err != nil {
		return nil, nil, err
	}
	if err := validateParsedOpenAIResponsesRequest(&req); err != nil {
		return nil, nil, err
	}
	return &req, body, nil
}

func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	var responsesReq *OpenAIResponsesRequest
	var responsesRawBody []byte
	_, route, ok := s.handlePOSTEndpoint(
		w,
		r,
		"OpenAI responses endpoint",
		func(r *http.Request) (*endpointRequestInfo, error) {
			req, rawBody, err := s.parseOpenAIResponsesRequest(r)
			if err != nil {
				return nil, err
			}
			responsesReq = req
			responsesRawBody = rawBody
			return &endpointRequestInfo{
				Model:     req.Model,
				Stream:    req.Stream,
				Payload:   req,
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
		return
	}

	ctx := r.Context()
	responsesReq.Model = route.MappedModel

	if route.Provider == constants.ProviderOpenAI {
		if route.MappedModel != route.OriginalModel {
			responsesRawBody = rewriteResponsesRequestModel(responsesRawBody, route.MappedModel)
		}
		s.forwardOpenAIResponsesDirectly(w, r, responsesReq, responsesRawBody, route.OriginalModel)
		return
	}

	typed, err := OpenAIResponsesRequestToTyped(ctx, responsesReq)
	if err != nil {
		logger.From(ctx).Errorf("Failed to convert OpenAI responses request: %v", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "conversion_error", http.StatusBadRequest)
		return
	}
	typedCurrent := typed

	if responsesBackgroundRequested(responsesReq) {
		if responsesReq.Stream {
			s.sendOpenAIError(w, ErrTypeInvalidRequest, "background responses cannot be streamed by the compatibility layer", "", http.StatusBadRequest)
			return
		}
		s.handleConvertedOpenAIResponsesBackground(w, r, responsesReq, typedCurrent, route)
		return
	}

	stateCtx, typedWithState, err := s.prepareOpenAIResponsesStateWithMode(ctx, responsesReq, typed, route.OriginalModel, responsesStateForeground, responsesStoreRequested(responsesReq))
	if err != nil {
		logger.From(ctx).Errorf("Failed to prepare OpenAI responses state: %v", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "state_error", http.StatusBadRequest)
		return
	}
	typed = typedWithState

	if responsesReq.Stream {
		s.handleConvertedOpenAIResponsesStream(w, r, responsesReq, typed, typedCurrent, stateCtx, route.Provider, route.MappedModel, route.OriginalModel)
		return
	}

	upstreamResp, err := s.forwardTypedAsAnthropic(ctx, typed, route.Provider, route.MappedModel, route.OriginalModel)
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, route.Provider, err)
		return
	}
	registry := responseToolNameRegistryFromCoreTools(typed.Tools)
	resp := s.converter.ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(upstreamResp, route.OriginalModel, registry)
	if err := s.commitOpenAIResponsesStateWithBlocks(ctx, stateCtx, responsesReq, typedCurrent, resp, route.OriginalModel, AnthropicBlocksToCoreWithToolNameRegistry(upstreamResp.Content, registry)); err != nil {
		logger.From(ctx).Errorf("Failed to save OpenAI responses state: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to save response state", "state_error", http.StatusInternalServerError)
		return
	}
	logger.DebugJSON(logger.From(ctx), "Sending OpenAI responses response", resp)
	_ = s.sendJSONResponse(ctx, w, resp)
}

func (s *Server) forwardTypedAsAnthropic(ctx context.Context, typed TypedRequest, provider, mappedModel, originalModel string) (*AnthropicResponse, error) {
	typed.Stream = false
	if err := s.validateConvertedOpenAIChatToolSequence(typed, provider, mappedModel); err != nil {
		return nil, err
	}
	if s.useNativeArgoOpenAIChatRoute(provider, mappedModel) {
		openAIReq, err := renderTypedToOpenAIRequest(typed, typedRenderContext{Model: mappedModel, OpenAIChatCompatibilityTools: true})
		if err != nil {
			return nil, err
		}
		openAIReq.Model = mappedModel
		normalizeArgoOpenAIChatRequest(openAIReq)
		var openAIResp OpenAIResponse
		if err := s.doJSON(ctx, s.endpoints.ArgoOpenAI, openAIReq, s.configureArgoOpenAIRequest, &openAIResp, "Argo OpenAI"); err != nil {
			return nil, err
		}
		return s.converter.ConvertOpenAIToAnthropicWithToolNameRegistry(&openAIResp, originalModel, responseToolNameRegistryFromCoreTools(typed.Tools)), nil
	}
	typed = ensureResponsesAnthropicWireMaxTokens(typed, provider, mappedModel)
	anthReq, err := TypedToAnthropicRequest(typed, mappedModel)
	if err != nil {
		return nil, err
	}
	anthReq.Model = mappedModel
	return s.forwardAnthropicRequest(ctx, anthReq, provider, originalModel)
}

func (s *Server) sendOpenAIResponsesRequest(ctx context.Context, reqBody *OpenAIResponsesRequest, rawBody []byte, stream bool) (*http.Response, error) {
	extraHeaders := map[string]string{}
	if stream {
		extraHeaders["Accept"] = "text/event-stream"
	}
	resp, _, err := s.sendProviderJSONRequest(ctx, providerJSONRequest{
		URL:          s.endpoints.OpenAIResponses,
		Provider:     constants.ProviderOpenAI,
		RequestName:  "OpenAI responses",
		Payload:      reqBody,
		RawBody:      rawBody,
		ExtraHeaders: extraHeaders,
		Configure: func(req *http.Request) error {
			return auth.ApplyProviderCredentials(req, constants.ProviderOpenAI, s.config.OpenAIAPIKey)
		},
	})
	return resp, err
}

func (s *Server) forwardOpenAIResponsesDirectly(w http.ResponseWriter, r *http.Request, responsesReq *OpenAIResponsesRequest, rawBody []byte, originalModel string) {
	ctx := r.Context()
	log := logger.From(ctx)
	if s.endpoints.OpenAIResponses == "" {
		log.Errorf("OpenAI responses URL not configured")
		s.sendOpenAIError(w, ErrTypeServer, "OpenAI responses URL not configured", "configuration_error", http.StatusInternalServerError)
		return
	}
	if responsesReq.Stream {
		s.forwardOpenAIResponsesStreamDirectly(w, r, responsesReq, rawBody, originalModel)
		return
	}

	resp, err := s.sendOpenAIResponsesRequest(ctx, responsesReq, rawBody, false)
	if err != nil {
		log.Errorf("OpenAI responses request failed: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := s.readResponseBody(resp)
	if err != nil {
		log.Errorf("Failed to read OpenAI responses response: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to read response", "read_error", http.StatusBadGateway)
		return
	}
	if resp.StatusCode >= 400 {
		passthroughErrorResponse(ctx, w, constants.ProviderOpenAI, resp.StatusCode, respBody)
		return
	}

	visibleModel := clientVisibleCreatedResponsesModel(responsesReq.Model, originalModel)
	s.registerResponsesModelAliasFromBody(respBody, responsesReq.Model, visibleModel)
	body := rewriteResponsesBodyModel(respBody, visibleModel)
	body = append(bytes.TrimRight(body, "\n"), '\n')
	w.Header().Set("Content-Type", "application/json")
	logWireBytes(ctx, "WIRE CLIENT RESPONSE BODY", body)
	if _, err := w.Write(body); err != nil {
		log.Errorf("Failed to write OpenAI responses response: %v", err)
	}
}

func (s *Server) forwardOpenAIResponsesStreamDirectly(w http.ResponseWriter, r *http.Request, responsesReq *OpenAIResponsesRequest, rawBody []byte, originalModel string) {
	ctx := r.Context()
	log := logger.From(ctx)
	resp, err := s.sendOpenAIResponsesRequest(ctx, responsesReq, rawBody, true)
	if err != nil {
		log.Errorf("OpenAI responses streaming request failed: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := s.readErrorBody(resp)
		passthroughErrorResponse(ctx, w, constants.ProviderOpenAI, resp.StatusCode, body)
		return
	}
	visibleModel := clientVisibleCreatedResponsesModel(responsesReq.Model, originalModel)

	setSSEHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Errorf("ResponseWriter does not support flushing")
		return
	}
	scanner := NewSSEScanner(resp.Body)
	for scanner.Scan() {
		line := s.rewriteResponsesStreamModel(scanner.Text(), responsesReq.Model, visibleModel)
		payload := line + "\n"
		if strings.HasPrefix(line, "data: ") || line == "" {
			payload = line + "\n"
		}
		logWireBytes(ctx, "WIRE CLIENT STREAM", []byte(payload))
		fmt.Fprint(w, payload)
		flusher.Flush()
	}
	if err := scanner.Err(); err != nil {
		_ = handleStreamError(ctx, nil, "OpenAIResponsesDirectSSEScanner", err)
	}
}

func (s *Server) rewriteResponsesStreamModel(line, mappedModel, visibleModel string) string {
	if !strings.HasPrefix(line, "data: ") || strings.TrimSpace(line) == "data: [DONE]" {
		return line
	}
	data := strings.TrimPrefix(line, "data: ")
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(data), &decoded); err != nil {
		return line
	}
	changed := false
	if _, ok := decoded["model"]; ok {
		decoded["model"] = visibleModel
		changed = true
	}
	if id, ok := decoded["id"].(string); ok {
		s.registerResponsesModelAlias(id, mappedModel, visibleModel)
	}
	if response, ok := decoded["response"].(map[string]interface{}); ok {
		if _, ok := response["model"]; ok {
			response["model"] = visibleModel
			changed = true
		}
		if id, ok := response["id"].(string); ok {
			s.registerResponsesModelAlias(id, mappedModel, visibleModel)
		}
	}
	if !changed {
		return line
	}
	updated, err := json.Marshal(decoded)
	if err != nil {
		return line
	}
	return "data: " + string(updated)
}

func rewriteResponsesBodyModel(body []byte, originalModel string) []byte {
	if originalModel == "" {
		return body
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return body
	}
	if _, ok := decoded["model"]; !ok {
		return body
	}
	decoded["model"] = originalModel
	updated, err := json.Marshal(decoded)
	if err != nil {
		return body
	}
	return updated
}

func rewriteResponsesRequestModel(body []byte, mappedModel string) []byte {
	if mappedModel == "" {
		return body
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return body
	}
	decoded["model"] = mappedModel
	updated, err := json.Marshal(decoded)
	if err != nil {
		return body
	}
	return updated
}

func clientVisibleCreatedResponsesModel(mappedModel, originalModel string) string {
	mappedModel = strings.TrimSpace(mappedModel)
	originalModel = strings.TrimSpace(originalModel)
	if originalModel == "" {
		return mappedModel
	}
	return originalModel
}

func (s *Server) registerResponsesModelAliasFromBody(body []byte, mappedModel, visibleModel string) {
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return
	}
	if id, ok := decoded["id"].(string); ok {
		s.registerResponsesModelAlias(id, mappedModel, visibleModel)
	}
}

func (s *Server) registerResponsesModelAlias(responseID, mappedModel, visibleModel string) {
	responseID = strings.TrimSpace(responseID)
	mappedModel = strings.TrimSpace(mappedModel)
	visibleModel = strings.TrimSpace(visibleModel)
	if responseID == "" || mappedModel == "" || visibleModel == "" || mappedModel == visibleModel {
		return
	}
	s.responsesModelAliasMu.Lock()
	defer s.responsesModelAliasMu.Unlock()
	if s.responsesModelAliases == nil {
		s.responsesModelAliases = make(map[string]string)
	}
	s.responsesModelAliases[responseID] = visibleModel
}

func (s *Server) clientVisibleResponsesModel(responseID, upstreamModel string) string {
	responseID = strings.TrimSpace(responseID)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return upstreamModel
	}
	if responseID == "" {
		return upstreamModel
	}
	s.responsesModelAliasMu.RLock()
	alias := s.responsesModelAliases[responseID]
	s.responsesModelAliasMu.RUnlock()
	if alias != "" {
		return alias
	}
	return upstreamModel
}

func (s *Server) handleConvertedOpenAIResponsesStream(w http.ResponseWriter, r *http.Request, responsesReq *OpenAIResponsesRequest, typed TypedRequest, typedCurrent TypedRequest, stateCtx *openAIResponsesStateContext, provider, mappedModel, originalModel string) {
	ctx := r.Context()
	if err := s.validateConvertedOpenAIChatToolSequence(typed, provider, mappedModel); err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, provider, err)
		return
	}
	if s.useNativeArgoOpenAIChatRoute(provider, mappedModel) {
		openAIReq, err := renderTypedToOpenAIRequest(typed, typedRenderContext{Model: mappedModel, OpenAIChatCompatibilityTools: true})
		if err != nil {
			s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "conversion_error", http.StatusBadRequest)
			return
		}
		openAIReq.Model = mappedModel
		openAIReq.Stream = true
		writer, ok := s.newConfiguredResponsesStreamWriter(ctx, w, stateCtx, originalModel, typed)
		if !ok {
			return
		}
		resp, blocks, err := s.streamResponsesFromArgoOpenAIRequest(ctx, openAIReq, writer)
		if err != nil {
			if !writer.started {
				s.sendProviderErrorAsOpenAI(ctx, w, provider, err)
				return
			}
			s.failAndCommitOpenAIResponsesStream(ctx, stateCtx, responsesReq, typedCurrent, writer, err, originalModel)
			return
		}
		if err := s.commitOpenAIResponsesStateWithBlocks(ctx, stateCtx, responsesReq, typedCurrent, resp, originalModel, blocks); err != nil {
			logger.From(ctx).Errorf("Failed to save OpenAI responses stream state: %v", err)
		}
		return
	}
	typed = ensureResponsesAnthropicWireMaxTokens(typed, provider, mappedModel)
	anthReq, err := TypedToAnthropicRequest(typed, mappedModel)
	if err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "conversion_error", http.StatusBadRequest)
		return
	}
	anthReq.Model = mappedModel
	anthReq.Stream = true

	writer, ok := s.newConfiguredResponsesStreamWriter(ctx, w, stateCtx, originalModel, typed)
	if !ok {
		return
	}
	resp, blocks, err := s.streamResponsesFromProvider(ctx, anthReq, provider, originalModel, writer)
	if err != nil {
		if !writer.started {
			s.sendProviderErrorAsOpenAI(ctx, w, provider, err)
			return
		}
		s.failAndCommitOpenAIResponsesStream(ctx, stateCtx, responsesReq, typedCurrent, writer, err, originalModel)
		return
	}
	if err := s.commitOpenAIResponsesStateWithBlocks(ctx, stateCtx, responsesReq, typedCurrent, resp, originalModel, blocks); err != nil {
		logger.From(ctx).Errorf("Failed to save OpenAI responses stream state: %v", err)
		return
	}
}

func (s *Server) newConfiguredResponsesStreamWriter(ctx context.Context, rw http.ResponseWriter, stateCtx *openAIResponsesStateContext, originalModel string, typed TypedRequest) (*responsesStreamWriter, bool) {
	writer, err := newResponsesStreamWriter(rw, ctx, originalModel)
	if err != nil {
		logger.From(ctx).Errorf("Failed to initialize OpenAI responses stream: %v", err)
		s.sendOpenAIError(rw, ErrTypeServer, "Failed to initialize streaming", "stream_init_error", http.StatusInternalServerError)
		return nil, false
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		writer.SetConversationID(stateCtx.Conversation.ID)
	}
	writer.SetToolNameRegistry(responseToolNameRegistryFromCoreTools(typed.Tools))
	return writer, true
}

func (s *Server) failAndCommitOpenAIResponsesStream(ctx context.Context, stateCtx *openAIResponsesStateContext, responsesReq *OpenAIResponsesRequest, typedCurrent TypedRequest, writer *responsesStreamWriter, streamErr error, originalModel string) {
	logResponsesStreamError(ctx, streamErr)
	resp, failErr := writer.Fail(streamErr)
	if failErr != nil {
		logger.From(ctx).Errorf("Failed to send OpenAI responses stream failure event: %v", failErr)
	}
	if resp == nil {
		return
	}
	if err := s.commitOpenAIResponsesStateWithBlocks(context.WithoutCancel(ctx), stateCtx, responsesReq, typedCurrent, resp, originalModel, writer.Blocks()); err != nil {
		logger.From(ctx).Errorf("Failed to save failed OpenAI responses stream state: %v", err)
	}
}
