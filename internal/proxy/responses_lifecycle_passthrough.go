package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) forwardOpenAIRawLifecycle(w http.ResponseWriter, r *http.Request, family string) {
	ctx := r.Context()
	body, err := s.readRequestBody(r)
	if err != nil {
		s.sendOpenAIRequestError(w, err)
		return
	}
	logWireClientRequest(ctx, r, body)
	s.forwardOpenAIRawLifecycleWithBody(w, r, family, body)
}

func (s *Server) forwardOpenAIRawLifecycleWithBody(w http.ResponseWriter, r *http.Request, family string, body []byte) {
	ctx := r.Context()
	passthrough, ok := s.responsesPassthroughTarget()
	if !ok {
		s.sendOpenAIError(w, ErrTypeServer, "Responses URL not configured", "configuration_error", http.StatusInternalServerError)
		return
	}
	logName := passthrough.logName("lifecycle")
	target, err := rawLifecycleURL(passthrough.URL, r, family)
	if err != nil {
		logger.From(ctx).Errorf("%s URL: %v", logName, err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to create upstream request", "upstream_error", http.StatusBadGateway)
		return
	}
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
	passthrough.Configure(upstreamReq)
	logWireHTTPRequest(ctx, "WIRE BACKEND REQUEST "+logName, upstreamReq, body)
	resp, err := s.client.Do(ctx, upstreamReq, passthrough.Provider)
	if err != nil {
		logger.From(ctx).Errorf("%s request failed: %v", logName, err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	logWireHTTPResponseHeaders(ctx, "WIRE BACKEND RESPONSE HEADERS "+logName, resp)
	wrapWireLoggedResponseBody(ctx, "WIRE BACKEND RESPONSE BODY "+logName, resp)
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

// rawLifecycleURL maps a client lifecycle or conversations path onto the upstream
// Responses base URL, preserving the sub-path and query string. The conversations
// family assumes the upstream serves /conversations as a sibling of /responses,
// which holds for both OpenAI (/v1/...) and Argo (/argoapi/v1/...). The sub-path
// joins the base URL's path both decoded and as the client escaped it, so the URL
// escapes nothing again and an ID reaches the provider as the client named it,
// with any escaped '/' kept inside it. The client's query follows the resolved
// endpoint's query.
func rawLifecycleURL(responsesURL string, r *http.Request, family string) (string, error) {
	target, err := url.Parse(responsesURL)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(target.Path, "/")
	prefix := "/v1/responses"
	if family == "conversations" {
		base = strings.TrimSuffix(base, "/responses") + "/conversations"
		prefix = "/v1/conversations"
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	target.Path = base + rest
	target.RawPath = (&url.URL{Path: base}).EscapedPath() + escapedPathAfter(r.URL, len(r.URL.Path)-len(rest))
	if r.URL.RawQuery != "" {
		if target.RawQuery != "" {
			target.RawQuery += "&"
		}
		target.RawQuery += r.URL.RawQuery
	}
	return target.String(), nil
}

// escapedPathAfter returns the part of u's escaped path that follows the first
// n bytes of its decoded path, which decodes to the rest of the decoded path. An
// escaped path holds '%' only before two hex digits, which decode to one byte.
func escapedPathAfter(u *url.URL, n int) string {
	escaped := u.EscapedPath()
	i := 0
	for ; n > 0 && i < len(escaped); n-- {
		if escaped[i] == '%' {
			i += 3
		} else {
			i++
		}
	}
	return escaped[min(i, len(escaped)):]
}

func (s *Server) decodeOptionalJSON(r *http.Request, dst interface{}) error {
	body, err := s.readRequestBody(r)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	logWireClientRequest(r.Context(), r, body)
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid JSON in request body: %w", err)
	}
	return nil
}
