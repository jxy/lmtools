// Package core provides type-safe conversions between different LLM provider formats.
//
// ARCHITECTURAL OVERVIEW:
// This package implements a strongly-typed message system that replaces generic
// map[string]interface{} usage throughout the codebase. All provider-specific
// formats are converted to/from a canonical TypedMessage representation.
//
// UNION TYPE PATTERN:
// The package defines content union types (OpenAIContentUnion, AnthropicContentUnion)
// that handle the polymorphic nature of LLM API content fields, which can be either
// a simple string or an array of content objects.
//
// KEY DESIGN DECISIONS:
//
// 1. Union types CANNOT be marshaled directly:
//   - They implement UnmarshalJSON for parsing provider responses
//   - They deliberately fail MarshalJSON to prevent accidental misuse
//   - This compile-time safety prevents runtime errors
//
// 2. Request building uses dedicated marshal functions:
//   - MarshalAnthropicMessagesForRequest: Builds Anthropic API format
//   - MarshalOpenAIMessagesForRequest: Builds OpenAI API format
//   - MarshalGoogleMessagesForRequest: Builds Google API format
//   - These functions handle union types correctly via ToMap() methods
//
// 3. Type safety throughout:
//   - No map[string]interface{} in business logic
//   - Compile-time type checking for all conversions
//   - Clear error messages when misused
//
// USAGE EXAMPLES:
//
// Correct usage for request building:
//
//	messages := []TypedMessage{...}
//	openAIMessages := ToOpenAITyped(messages)
//	requestBody := MarshalOpenAIMessagesForRequest(openAIMessages)
//	jsonBytes, _ := json.Marshal(requestBody) // Safe!
//
// Incorrect usage (will fail at runtime):
//
//	messages := []OpenAIMessage{...} // Has OpenAIContentUnion inside
//	jsonBytes, _ := json.Marshal(messages) // RUNTIME ERROR!
//
// BEST PRACTICES:
// - Always use TypedMessage for internal processing
// - Convert to provider format only at API boundaries
// - Use Marshal*ForRequest functions for building requests
// - Never attempt to marshal union types directly
// - Validate union types with ValidateForMarshal() when needed
//
// ARCHITECTURAL NOTE: These conversion functions use strongly typed structures
// instead of map[string]interface{}. This ensures type safety and makes the
// code more maintainable. All conversions go through TypedMessage as the
// canonical internal representation.
//
// UNION TYPE HANDLING:
// This package defines content union types (OpenAIContentUnion, AnthropicContentUnion)
// that represent the different ways content can be structured in API requests.
// These union types intentionally fail MarshalJSON to prevent accidental direct
// marshaling. They are internal representations that must be converted to the
// appropriate format using the Marshal*ForRequest functions.
//
// IMPORTANT: Never marshal union types directly. Always use:
//   - MarshalAnthropicMessagesForRequest for Anthropic API format
//   - MarshalOpenAIMessagesForRequest for OpenAI API format
//   - MarshalGoogleMessagesForRequest for Google API format
//
// These marshal functions handle the union types correctly and produce
// the exact JSON structure expected by each provider's API.
package core
