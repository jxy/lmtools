package proxy

import (
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"time"
)

type argoStreamRead struct {
	data []byte
	err  error
}

// ArgoStreamParser handles Argo streaming responses.
type ArgoStreamParser struct {
	handler *AnthropicStreamHandler
}

// NewArgoStreamParser creates a new Argo stream parser.
func NewArgoStreamParser(handler *AnthropicStreamHandler) *ArgoStreamParser {
	return &ArgoStreamParser{handler: handler}
}

// Parse parses an Argo streaming response.
func (p *ArgoStreamParser) Parse(reader io.Reader) error {
	return p.ParseWithPingInterval(reader, constants.DefaultPingInterval*time.Second)
}

// ParseWithPingInterval parses an Argo streaming response with configurable ping interval.
func (p *ArgoStreamParser) ParseWithPingInterval(reader io.Reader, pingInterval time.Duration) error {
	buffer := make([]byte, 1024)
	lastActivity := time.Now()
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	events := make(chan argoStreamRead, 1)
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])
				select {
				case events <- argoStreamRead{data: data}:
				case <-stop:
					return
				case <-p.handler.ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case events <- argoStreamRead{err: err}:
				case <-stop:
				case <-p.handler.ctx.Done():
				}
				return
			}
		}
	}()

	for {
		select {
		case <-p.handler.ctx.Done():
			return p.handler.ctx.Err()

		case event := <-events:
			if len(event.data) > 0 {
				lastActivity = time.Now()
				text := string(event.data)
				logger.From(p.handler.ctx).Debugf("Argo Stream Chunk: %q", text)
				if err := p.handler.SendTextDelta(text); err != nil {
					return handleStreamError(p.handler.ctx, p.handler, "ArgoStreamParser", err)
				}
				p.handler.state.OutputTokens += EstimateTokenCount(text)
			}

			if event.err != nil {
				if event.err == io.EOF {
					return p.handler.Complete("end_turn")
				}
				return handleStreamError(p.handler.ctx, p.handler, "ArgoStreamParser", event.err)
			}

		case <-pingTicker.C:
			if time.Since(lastActivity) >= pingInterval {
				logger.From(p.handler.ctx).Debugf("Sending ping after %v of inactivity", time.Since(lastActivity))
				if err := p.handler.SendPing(); err != nil {
					return handleStreamError(p.handler.ctx, p.handler, "ArgoStreamParser:ping",
						fmt.Errorf("client disconnected: %w", err))
				}
			}
		}
	}
}
