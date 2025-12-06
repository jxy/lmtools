package proxy

import (
	"fmt"
	"net/url"
	"strings"
)

// apiV1Path is the standard Argo API path prefix.
const apiV1Path = "/api/v1"

// argoEndpoints encapsulates all Argo URL endpoint construction.
// This is the single source of truth for Argo URL path normalization.
// All Argo endpoints are derived from a base URL through this struct.
type argoEndpoints struct {
	baseAPI  string // Base API path ending in /api/v1
	resource string // baseAPI + "/resource"
	chat     string // resource + "/chat/"
	stream   string // resource + "/streamchat/"
	embed    string // resource + "/embed/"
	models   string // baseAPI + "/models/"
}

// newArgoEndpoints creates an argoEndpoints struct from a base URL.
// It normalizes the URL path to ensure /api/v1 appears exactly once,
// then derives all endpoint URLs from that base.
//
// The function handles various input formats:
//   - URLs with /api/v1 already present (truncates at /api/v1)
//   - URLs without /api/v1 (appends it)
//   - URLs with nested paths after /api/v1 (truncates them)
//   - Trailing slashes are normalized
//
// Returns an error if:
//   - The URL cannot be parsed
//   - The URL scheme is not http or https
func newArgoEndpoints(base string) (*argoEndpoints, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("invalid Argo base URL %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q, must be http or https", u.Scheme)
	}

	// Normalize path: ensure /api/v1 appears exactly once at the end
	path := strings.TrimRight(u.Path, "/")
	if idx := strings.Index(path, apiV1Path); idx != -1 {
		// Truncate at /api/v1 (handles nested paths like /api/v1/resource)
		path = path[:idx+len(apiV1Path)]
	} else {
		// No /api/v1 found - append it
		path = path + apiV1Path
	}

	// Build base API URL
	u.Path = path
	baseAPI := u.String()

	// Build resource prefix (for chat, streaming, embeddings)
	resource := strings.TrimRight(baseAPI, "/") + "/resource"

	// Build all endpoints
	// Argo API requires trailing slashes on all endpoints
	return &argoEndpoints{
		baseAPI:  baseAPI,
		resource: resource,
		chat:     resource + "/chat/",
		stream:   resource + "/streamchat/",
		embed:    resource + "/embed/",
		models:   baseAPI + "/models/",
	}, nil
}
