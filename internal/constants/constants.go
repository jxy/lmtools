package constants

// Filesystem permissions - using principle of least privilege
const (
	// DirPerm is the permission for all created directories (rwx------)
	// Only the owner can read, write, and execute (navigate)
	DirPerm = 0o700

	// FilePerm is the permission for all created files (rw-------)
	// Only the owner can read and write
	FilePerm = 0o600
)

// Streaming constants
const (
	// DefaultStreamParseErrorThreshold is the number of parse errors before warning
	DefaultStreamParseErrorThreshold = 5

	// DefaultTextChunkSize is the default chunk size for text streaming
	DefaultTextChunkSize = 20

	// DefaultJSONChunkSize is the default chunk size for JSON streaming
	// Increased to reduce the number of partial_json events while keeping granularity
	DefaultJSONChunkSize = 64

	// DefaultPingInterval is the default interval for sending ping events during streaming
	// This is set to 15 seconds for production use to avoid excessive network traffic
	DefaultPingInterval = 15 // seconds

	// IncludeUsageKey is the metadata key for requesting usage information in streaming responses
	// Used in OpenAI-compatible streaming to include token usage in the final chunk
	IncludeUsageKey = "stream_options.include_usage"
)

// HTTP Request/Response size limits
const (
	// DefaultMaxRequestBodySize is the default maximum size for HTTP request bodies
	DefaultMaxRequestBodySize = 10 * 1024 * 1024 // 10MB

	// DefaultMaxResponseBodySize is the default maximum size for HTTP response bodies
	DefaultMaxResponseBodySize = 20 * 1024 * 1024 // 20MB

	// MaxErrorResponseSize is the maximum size for error response bodies
	MaxErrorResponseSize = 10 * 1024 // 10KB

	// MaxCLIInputSize is the maximum input size for CLI operations
	MaxCLIInputSize = 10 * 1024 * 1024 // 10MB

	// MaxCLIResponseSize is the maximum response size for CLI operations
	MaxCLIResponseSize = 10 * 1024 * 1024 // 10MB

	// MaxStreamingResponseSize is the maximum size for streaming response bodies
	MaxStreamingResponseSize = 100 * 1024 * 1024 // 100MB
)

// Provider constants
const (
	ProviderArgo      = "argo"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGoogle    = "google"
)
