package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"strings"
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

	var payload strings.Builder
	if eventType != "" {
		fmt.Fprintf(&payload, "event: %s\n", eventType)
	}
	fmt.Fprintf(&payload, "data: %s\n\n", data)
	raw := payload.String()
	logWireBytes(s.ctx, "WIRE CLIENT STREAM", []byte(raw))
	if _, err := io.WriteString(s.w, raw); err != nil {
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
