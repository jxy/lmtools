// Package core provides the business logic for building and handling LLM API requests and responses.
//
// # Overview
//
// The core package implements a unified message model and request/response handling system that works
// across multiple LLM providers (Anthropic, OpenAI, Google/Gemini, Argo). It provides a single,
// consistent interface for:
//
//   - Building API requests with proper authentication and formatting
//   - Handling streaming and non-streaming responses
//   - Managing tool calls and executions
//   - Converting between provider-specific formats
//
// # Key Concepts
//
// TypedMessage and Blocks:
//
// The package uses a unified TypedMessage model that represents conversations as a series of
// messages with typed content blocks. This allows consistent handling of:
//   - Text content (TextBlock)
//   - Tool calls (ToolUseBlock)
//   - Tool results (ToolResultBlock)
//
// Each message has a role ("user", "assistant", "system") and contains one or more blocks.
// This model maps cleanly to all supported providers while maintaining type safety.
//
// Provider Adapters:
//
// Each provider has dedicated functions for:
//   - Request building (buildAnthropicRequest, buildOpenAIRequest, etc.)
//   - Response parsing (parseAnthropicResponse, parseOpenAIResponse, etc.)
//   - Streaming handling (streamAnthropicHandler, streamOpenAIHandler, etc.)
//
// The adapters handle provider-specific details like header formats, endpoint URLs, and
// message structure differences.
//
// Tool Support:
//
// The package provides first-class support for tool execution through:
//   - ToolDefinition: Describes available tools with JSON schemas
//   - ToolCall: Represents a request to execute a tool
//   - ToolResult: Contains the output from tool execution
//   - Executor: Handles secure command execution with approval policies
//
// Tools are currently limited to the universal_command tool which allows system command
// execution with proper security controls (whitelisting, blacklisting, approval flows).
//
// # Usage
//
// The main entry points are:
//   - BuildRequest: Builds a chat completion request
//   - BuildRequestWithToolInteractions: Builds a request including tool interactions from session
//   - ParseResponse: Parses non-streaming responses
//   - ParseStreamResponse: Handles streaming responses with a callback
//
// Example:
//
//	cfg := RequestConfig{...}
//	messages := []Message{{Role: "user", Content: "Hello"}}
//	req, body, err := BuildRequest(cfg, messages)
//	if err != nil {
//	    return err
//	}
//	// Send request and handle response...
//
// # Error Handling
//
// The package uses explicit error returns rather than panics. Common errors include:
//   - Provider not supported for requested operation (e.g., embeddings)
//   - Tool execution denied by security policy
//   - Invalid message format for provider
//   - API authentication failures
//
// All errors include context about what operation failed and why.
package core
