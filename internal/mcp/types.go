// Package mcp is a Model Context Protocol client on the standard library:
// the JSON-RPC envelope, the stdio and Streamable HTTP transports, the
// modern era (revision 2026-07-28, per request metadata) and the legacy era
// (an initialize handshake), and the tool list and call operations lmc
// uses. It knows nothing about lmc's tool loop; internal/core adapts it.
package mcp

import "encoding/json"

// Protocol revisions this client speaks. Revisions are dates, so they order
// as strings, and 2026-07-28 is the first without a handshake.
const (
	ProtocolVersion20260728 = "2026-07-28"
	ProtocolVersion20251125 = "2025-11-25"
	ProtocolVersion20250618 = "2025-06-18"
	ProtocolVersion20250326 = "2025-03-26"
)

// ModernVersions carry version, identity, and capabilities on every request.
// LegacyVersions establish them once with initialize. Both are newest first.
var (
	ModernVersions = []string{ProtocolVersion20260728}
	LegacyVersions = []string{ProtocolVersion20251125, ProtocolVersion20250618, ProtocolVersion20250326}
)

// IsModernVersion reports whether a revision is in the per request era.
func IsModernVersion(version string) bool {
	return version >= ProtocolVersion20260728
}

// JSON-RPC error codes the client reads. The -3202x codes are the ones the
// 2026-07-28 revision reserves; a server that answers with one of them is
// modern whatever else it said.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	CodeHeaderMismatch                  = -32020
	CodeMissingRequiredClientCapability = -32021
	CodeUnsupportedProtocolVersion      = -32022
)

// Reserved _meta keys of the modern era.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// Result types a modern result declares. An absent resultType, from a
// legacy server, reads as complete.
const (
	ResultTypeComplete      = "complete"
	ResultTypeInputRequired = "input_required"
)

// Implementation names one side of the connection.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// Tool is one entry of a tools/list result.
type Tool struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description,omitempty"`
	InputSchema  json.RawMessage  `json:"inputSchema"`
	OutputSchema json.RawMessage  `json:"outputSchema,omitempty"`
	Annotations  *ToolAnnotations `json:"annotations,omitempty"`
	Meta         json.RawMessage  `json:"_meta,omitempty"`
}

// ToolAnnotations are the server's hints about a tool. The specification
// says a client must treat them as untrusted, and nothing in lmc grants on
// them.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// Content is one item of a tool result's content array: text, image, audio,
// resource_link, or resource (embedded). The fields each type uses are
// documented on the specification's tools page.
type Content struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Data        string            `json:"data,omitempty"`
	MimeType    string            `json:"mimeType,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Name        string            `json:"name,omitempty"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Resource    *ResourceContents `json:"resource,omitempty"`
}

// ResourceContents is an embedded resource: text or a base64 blob.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// CallToolResult is what tools/call returns. InputRequests and RequestState
// are set when a modern server answers input_required instead of a result;
// this client declares no capability a server could ask through, so seeing
// one is the server's error, reported to the model as such.
type CallToolResult struct {
	ResultType        string          `json:"resultType,omitempty"`
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	InputRequests     json.RawMessage `json:"inputRequests,omitempty"`
	RequestState      string          `json:"requestState,omitempty"`
	Meta              json.RawMessage `json:"_meta,omitempty"`
}

// InputRequired reports whether the server asked for more input instead of
// completing the call.
func (r *CallToolResult) InputRequired() bool {
	return r.ResultType == ResultTypeInputRequired
}

// ListToolsResult is one page of tools/list.
type ListToolsResult struct {
	ResultType string `json:"resultType,omitempty"`
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
	TTLMs      int64  `json:"ttlMs,omitempty"`
	CacheScope string `json:"cacheScope,omitempty"`
}

// DiscoverResult answers server/discover on a modern server.
type DiscoverResult struct {
	ResultType        string                     `json:"resultType,omitempty"`
	SupportedVersions []string                   `json:"supportedVersions"`
	Capabilities      map[string]json.RawMessage `json:"capabilities"`
	Instructions      string                     `json:"instructions,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
}

// InitializeResult answers initialize on a legacy server.
type InitializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	ServerInfo      Implementation             `json:"serverInfo"`
	Instructions    string                     `json:"instructions,omitempty"`
}

// Logf receives the client's debug lines. A nil Logf drops them.
type Logf func(format string, args ...interface{})

// serverLogf prefixes every line with the server it concerns, and swallows
// them when nobody is listening, so callers log without a nil check.
func serverLogf(log Logf, server string) Logf {
	if log == nil {
		return func(string, ...interface{}) {}
	}
	return func(format string, args ...interface{}) {
		log("mcp[%s]: "+format, append([]interface{}{server}, args...)...)
	}
}
