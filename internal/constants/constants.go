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

// Provider constants
const (
	ProviderArgo      = "argo"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGoogle    = "google"
)
