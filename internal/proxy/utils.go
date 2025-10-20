package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/errors"
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

// readResponseBody safely reads a response body with size limit
func (s *Server) readResponseBody(resp *http.Response) ([]byte, error) {
	maxSize := s.config.MaxResponseBodySize
	if maxSize <= 0 {
		maxSize = 50 * 1024 * 1024 // Default 50MB
	}

	limitedReader := io.LimitReader(resp.Body, maxSize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	if int64(len(body)) > maxSize {
		return nil, errors.WrapError("read response body", fmt.Errorf("response body too large: exceeds %d bytes", maxSize))
	}

	return body, nil
}

// truncateErrorBody safely truncates error response body for inclusion in error messages
func truncateErrorBody(body string, maxLen int) string {
	if len(body) <= maxLen {
		return body
	}
	// Truncate and add ellipsis
	return body[:maxLen] + "..."
}

// logErrorResponse logs error responses from providers
func (s *Server) logErrorResponse(ctx context.Context, provider string, statusCode int, body []byte) {
	log := logger.From(ctx)

	// Truncate body for logging if too large
	bodyStr := string(body)
	if len(bodyStr) > 1000 {
		bodyStr = bodyStr[:1000] + "... (truncated)"
	}

	log.Errorf("Provider %s returned error: status=%d, body=%s", provider, statusCode, bodyStr)
}

// extractRequestLogger is a helper function to extract the request logger from context
// and adapt it to the retry.Logger interface
func extractRequestLogger(ctx context.Context) retry.Logger {
	return &retryLoggerAdapter{ctx: ctx}
}

// doJSON is a generic helper for making JSON API requests to providers
// It handles marshaling, request creation, header application, execution, and unmarshaling
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
		return errors.WrapError(fmt.Sprintf("marshal %s request", provider), err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqData))
	if err != nil {
		return errors.WrapError(fmt.Sprintf("create %s request", provider), err)
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
		return errors.WrapError(fmt.Sprintf("send %s request", provider), err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := s.readResponseBody(resp)
	if err != nil {
		return err
	}

	// Check status
	if resp.StatusCode != http.StatusOK {
		s.logErrorResponse(ctx, provider, resp.StatusCode, body)
		return NewResponseError(resp.StatusCode, string(body))
	}

	// Parse response
	if err := json.Unmarshal(body, respBody); err != nil {
		return errors.WrapError(fmt.Sprintf("parse %s response", provider), err)
	}

	// Log response if debug enabled
	logger.DebugJSON(log, fmt.Sprintf("%s response", provider), respBody)

	return nil
}

// ResponseError represents an HTTP response error
type ResponseError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface
func (e *ResponseError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// NewResponseError creates a new ResponseError
func NewResponseError(statusCode int, body string) *ResponseError {
	return &ResponseError{
		StatusCode: statusCode,
		Body:       body,
	}
}
