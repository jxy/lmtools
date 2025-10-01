package proxy

import (
	"context"
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

// Server represents the API proxy server
type Server struct {
	config    *Config
	mapper    *ModelMapper
	converter *Converter
	client    *retry.Client
}

// NewServer creates a new API proxy server
func NewServer(config *Config) http.Handler {
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClientWithOptions(10*time.Minute, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger),
	}

	// Wrap with consolidated middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	return handler
}
