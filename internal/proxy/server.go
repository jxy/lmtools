package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"time"
)

// retryLoggerAdapter adapts the context-aware logger to retry.Logger interface
type retryLoggerAdapter struct {
	ctx context.Context
}

func (r *retryLoggerAdapter) Infof(format string, args ...interface{}) {
	if r.ctx != nil {
		logger.From(r.ctx).Infof(format, args...)
	} else {
		// Fallback when no request context is available
		logger.GetLogger().Infof(format, args...)
	}
}

func (r *retryLoggerAdapter) Debugf(format string, args ...interface{}) {
	if r.ctx != nil {
		logger.From(r.ctx).Debugf(format, args...)
	} else {
		// Fallback when no request context is available
		logger.GetLogger().Debugf(format, args...)
	}
}

func (r *retryLoggerAdapter) Errorf(format string, args ...interface{}) {
	if r.ctx != nil {
		logger.From(r.ctx).Errorf(format, args...)
	} else {
		// Fallback when no request context is available
		logger.GetLogger().Errorf(format, args...)
	}
}

const (
	// minPingInterval is the minimum allowed ping interval to prevent CPU spinning
	minPingInterval = 100 * time.Millisecond
	// maxPingInterval is the maximum allowed ping interval to prevent timeouts
	maxPingInterval = 60 * time.Second
)

// Server represents the API proxy server.
// It stores both config (for credentials, provider name, and settings) and
// endpoints (for precomputed URLs). All URL access should use s.endpoints.*,
// while credentials and configuration use s.config.*. Endpoints are always
// derived from Config via NewEndpoints() in NewServer.
type Server struct {
	config    *Config
	endpoints *Endpoints
	mapper    *ModelMapper
	converter *Converter
	client    *retry.Client
}

// NewServer creates a new API proxy server.
// It initializes endpoints and returns an error if initialization fails.
func NewServer(config *Config) (http.Handler, error) {
	// Create endpoints - single source of truth for URL initialization
	endpoints, err := NewEndpoints(config)
	if err != nil {
		return nil, fmt.Errorf("initialize endpoints: %w", err)
	}

	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		endpoints: endpoints,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClientWithOptions(10*time.Minute, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger),
	}

	// Wrap with consolidated middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	return handler, nil
}
