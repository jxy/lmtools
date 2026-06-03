// Package core contains provider-neutral request, response, message, and tool
// plumbing shared by lmc and apiproxy.
//
// The package's canonical conversation shape is []TypedMessage containing typed
// Block values such as TextBlock, ToolUseBlock, ToolResultBlock, and provider
// reasoning sidecars. Provider-specific JSON is kept at the edges: request
// builders render TypedMessage values through provider specs, and response
// handlers parse provider payloads back into Response and Block values.
//
// Main entry points:
//   - BuildRequest builds a request from CLI-style RequestOptions and input text.
//   - BuildChatRequest builds a chat request from already-typed messages.
//   - HandleResponse and HandleResponseWithOptions parse provider responses.
//   - HandleToolExecution runs tool-call loops using SessionStore boundaries.
//
// Provider rendering is implemented through provider specs and typed request
// builders in provider_specs*.go and request_builders.go. Keep new provider
// behavior flowing through typed structures rather than converting provider JSON
// directly into another provider's JSON.
package core
