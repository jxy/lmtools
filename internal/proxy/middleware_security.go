package proxy

import (
	"net/http"
)

// SecurityMiddleware adds security controls to requests
type SecurityMiddleware struct {
	next   http.Handler
	config *Config
}

// NewSecurityMiddleware creates a new security middleware
func NewSecurityMiddleware(next http.Handler, config *Config) http.Handler {
	return &SecurityMiddleware{
		next:   next,
		config: config,
	}
}

// ServeHTTP implements http.Handler
func (m *SecurityMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Apply request body size limit
	// This is applied here so it takes effect before any body reading
	r.Body = http.MaxBytesReader(w, r.Body, m.config.MaxRequestBodySize)

	// Continue to next handler
	m.next.ServeHTTP(w, r)
}
