package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

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
