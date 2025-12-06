package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/limitio"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
)

// CloseIdleConnections closes any idle HTTP connections
func (s *Server) CloseIdleConnections() {
	if s.client != nil && s.client.GetHTTPClient() != nil {
		if transport, ok := s.client.GetHTTPClient().Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
}

// readLimitedWithKind reads from an io.Reader with a size limit and descriptive error context.
func readLimitedWithKind(r io.Reader, maxSize int64, kind string) ([]byte, error) {
	return limitio.ReadLimitedWithKind(r, maxSize, kind)
}

// readResponseBody safely reads a response body with size limit
func (s *Server) readResponseBody(resp *http.Response) ([]byte, error) {
	maxSize := s.config.MaxResponseBodySize
	if maxSize <= 0 {
		maxSize = constants.DefaultMaxResponseBodySize
	}
	return readLimitedWithKind(resp.Body, maxSize, "response body")
}

// readErrorBody reads error response with fixed 10KB limit
// This is specifically for error responses where we want a smaller limit
func (s *Server) readErrorBody(resp *http.Response) ([]byte, error) {
	return readLimitedWithKind(resp.Body, constants.MaxErrorResponseSize, "error response")
}

// readStreamingBody reads a streaming response body with the streaming size limit.
// Use this for reading complete streaming responses that need to be buffered.
func (s *Server) readStreamingBody(r io.Reader) ([]byte, error) {
	return readLimitedWithKind(r, constants.MaxStreamingResponseSize, "streaming response")
}

// readRequestBody safely reads an HTTP request body with size limit
func (s *Server) readRequestBody(r *http.Request) ([]byte, error) {
	maxSize := s.config.MaxRequestBodySize
	if maxSize <= 0 {
		maxSize = constants.DefaultMaxRequestBodySize
	}
	return readLimitedWithKind(r.Body, maxSize, "request body")
}

// extractRequestLogger is a helper function to extract the request logger from context
// and adapt it to the retry.Logger interface
func extractRequestLogger(ctx context.Context) retry.Logger {
	return &retryLoggerAdapter{ctx: ctx}
}

// doJSON is a generic helper for making JSON API requests to providers.
// Logs full request/response at DEBUG level for troubleshooting (no truncation).
func (s *Server) doJSON(
	ctx context.Context,
	url string,
	reqBody interface{},
	headerFn func(*http.Request),
	respBody interface{},
	provider string,
) error {
	log := logger.From(ctx)

	// Marshal request
	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", provider, err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqData))
	if err != nil {
		return fmt.Errorf("create %s request: %w", provider, err)
	}

	// Set default content type
	req.Header.Set("Content-Type", "application/json")

	// Apply provider-specific headers
	if headerFn != nil {
		headerFn(req)
	}

	// Log request if debug enabled
	logger.DebugJSON(log, fmt.Sprintf("%s request", provider), reqBody)

	// Send request
	resp, err := s.client.Do(ctx, req, provider)
	if err != nil {
		return fmt.Errorf("send %s request: %w", provider, err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := s.readResponseBody(resp)
	if err != nil {
		return err
	}

	// Check status
	if resp.StatusCode != http.StatusOK {
		if log.IsDebugEnabled() {
			log.Debugf("Raw %s error response: %s", provider, string(body))
		}
		logErrorResponse(ctx, provider, resp.StatusCode, body)
		return NewResponseError(resp.StatusCode, string(body))
	}

	// Log raw response for debugging
	if log.IsDebugEnabled() {
		log.Debugf("Raw %s response: %s", provider, string(body))
	}

	// Parse response
	if err := json.Unmarshal(body, respBody); err != nil {
		if log.IsDebugEnabled() {
			log.Debugf("Raw %s response (parse failed): %s", provider, string(body))
		}
		return fmt.Errorf("parse %s response: %w", provider, err)
	}

	return nil
}

func (s *Server) sendJSONResponse(ctx context.Context, w http.ResponseWriter, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log the error but don't try to send another response
		// as headers may already be written
		logger.From(ctx).Errorf("Failed to encode JSON response: %v", err)
		return err
	}
	return nil
}

// setSSEHeaders sets standard headers for Server-Sent Events streaming.
// This is the single source of truth for SSE header configuration.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering
}

// passthroughErrorResponse writes an error response directly to the client.
// Use for pass-through behavior where the provider's error should be forwarded as-is.
// This logs the error and writes the response body unchanged.
func passthroughErrorResponse(ctx context.Context, w http.ResponseWriter, provider string, status int, body []byte) {
	logProviderErrorBody(ctx, provider, status, string(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
