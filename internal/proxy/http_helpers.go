package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/limitio"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"strings"
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
		maxSize = constants.DefaultMaxResponseBodySize
	}
	return limitio.ReadLimitedWithKind(resp.Body, maxSize, "response body")
}

// readErrorBody reads error response with fixed 10KB limit
// This is specifically for error responses where we want a smaller limit
func (s *Server) readErrorBody(resp *http.Response) ([]byte, error) {
	return limitio.ReadLimitedWithKind(resp.Body, constants.MaxErrorResponseSize, "error response")
}

// readDirectProviderResponse reads a non-stream provider response and forwards provider errors unchanged.
func (s *Server) readDirectProviderResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response, provider, responseName string) ([]byte, bool) {
	body, err := s.readResponseBody(resp)
	if err != nil {
		logger.From(ctx).Errorf("Failed to read %s response: %v", responseName, err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to read response", "read_error", http.StatusBadGateway)
		return nil, false
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		passthroughErrorResponse(ctx, w, provider, resp.StatusCode, body)
		return nil, false
	}
	return body, true
}

// readRequestBody safely reads an HTTP request body with size limit
func (s *Server) readRequestBody(r *http.Request) ([]byte, error) {
	maxSize := s.config.MaxRequestBodySize
	if maxSize <= 0 {
		maxSize = constants.DefaultMaxRequestBodySize
	}
	return limitio.ReadLimitedWithKind(r.Body, maxSize, "request body")
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
	configure func(*http.Request),
	respBody interface{},
	provider string,
	requestName ...string,
) error {
	name := provider
	if len(requestName) > 0 && requestName[0] != "" {
		name = requestName[0]
	}
	resp, _, err := s.sendProviderJSONRequest(ctx, providerJSONRequest{
		URL:         url,
		Provider:    provider,
		RequestName: name,
		Payload:     reqBody,
		Configure:   configure,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Read response body
	body, err := s.readResponseBody(resp)
	if err != nil {
		return err
	}

	// Check status
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return NewResponseError(resp.StatusCode, string(body))
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	// Parse response
	warnUnknownFields(ctx, body, respBody, name+" response")
	if err := json.Unmarshal(body, respBody); err != nil {
		return fmt.Errorf("parse %s response: %w", name, err)
	}

	return nil
}

func (s *Server) sendJSONResponse(ctx context.Context, w http.ResponseWriter, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(data)
	if err != nil {
		// Log the error but don't try to send another response
		// as headers may already be written
		logger.From(ctx).Errorf("Failed to encode JSON response: %v", err)
		return err
	}
	body = append(body, '\n')
	w.WriteHeader(http.StatusOK)
	logWireBytes(ctx, "WIRE CLIENT RESPONSE BODY", body)
	if _, err := w.Write(body); err != nil {
		logger.From(ctx).Errorf("Failed to write JSON response: %v", err)
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

type sseRecord struct {
	Lines     []string
	DataLines []string
}

func (r sseRecord) data() string {
	return strings.Join(r.DataLines, "\n")
}

func (r sseRecord) withData(data string) string {
	var payload strings.Builder
	dataWritten := false
	for _, line := range r.Lines {
		if _, ok := sseFieldValue(line, "data"); ok {
			if !dataWritten {
				writeSSEDataLines(&payload, data)
				dataWritten = true
			}
			continue
		}
		payload.WriteString(line)
		payload.WriteByte('\n')
	}
	payload.WriteByte('\n')
	return payload.String()
}

func sseFieldValue(line, field string) (string, bool) {
	prefix := field + ":"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(line, prefix)
	value = strings.TrimPrefix(value, " ")
	return value, true
}

func writeSSEDataLines(payload *strings.Builder, data string) {
	if data == "" {
		payload.WriteString("data: \n")
		return
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		fmt.Fprintf(payload, "data: %s\n", line)
	}
}

func consumeSSERecords(reader io.Reader, onRecord func(sseRecord) error) error {
	scanner := NewSSEScanner(reader)
	var record sseRecord

	flush := func() error {
		if len(record.Lines) == 0 {
			return nil
		}
		current := record
		record = sseRecord{}
		return onRecord(current)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		record.Lines = append(record.Lines, line)
		if data, ok := sseFieldValue(line, "data"); ok {
			record.DataLines = append(record.DataLines, data)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func forwardSSERecords(ctx context.Context, w http.ResponseWriter, reader io.Reader, transformData func(string) string) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("ResponseWriter does not support flushing")
	}
	return consumeSSERecords(reader, func(record sseRecord) error {
		payload := record.withData(transformData(record.data()))
		logClientStreamBytesIfUnhandled(ctx, w, []byte(payload))
		if _, err := io.WriteString(w, payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
}

func logClientStreamBytesIfUnhandled(ctx context.Context, w http.ResponseWriter, payload []byte) {
	// proxyResponseWriter already logs WIRE CLIENT STREAM in its own Write,
	// so skip here to avoid double-logging.
	if _, ok := w.(*proxyResponseWriter); ok {
		return
	}
	logWireBytes(ctx, "WIRE CLIENT STREAM", payload)
}

// passthroughErrorResponse writes an error response directly to the client.
// Use for pass-through behavior where the provider's error should be forwarded as-is.
// This logs the error and writes the response body unchanged.
func passthroughErrorResponse(ctx context.Context, w http.ResponseWriter, provider string, status int, body []byte) {
	logProviderErrorBody(ctx, provider, status, string(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	logWireBytes(ctx, "WIRE CLIENT RESPONSE BODY", body)
	_, _ = w.Write(body)
}
