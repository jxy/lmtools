package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
)

// SSEWriter handles Server-Sent Events writing.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
}

// NewSSEWriter creates a new SSE writer.
func NewSSEWriter(w http.ResponseWriter, ctx context.Context) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.From(ctx).Debugf("ResponseWriter type: %T does not implement http.Flusher", w)
		return nil, fmt.Errorf("streaming not supported (ResponseWriter type: %T)", w)
	}

	setSSEHeaders(w)
	return &SSEWriter{w: w, flusher: flusher, ctx: ctx}, nil
}

// WriteEvent writes an SSE event.
func (s *SSEWriter) WriteEvent(eventType, data string) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}

	if eventType != "" {
		logger.From(s.ctx).Debugf("→ CLIENT: event: %s | data: %s", eventType, data)
	} else {
		logger.From(s.ctx).Debugf("→ CLIENT: data: %s", data)
	}

	if eventType != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", eventType); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteJSON writes a JSON object as an SSE event.
func (s *SSEWriter) WriteJSON(eventType string, data interface{}) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.WriteEvent(eventType, string(jsonData))
}

// TrackEvent tracks an event that was sent to the client.
func (s *SSEWriter) TrackEvent(handler *AnthropicStreamHandler, eventType string) {
	if handler != nil && handler.state != nil && eventType != "" {
		handler.state.EventsSent = append(handler.state.EventsSent, eventType)
	}
}
