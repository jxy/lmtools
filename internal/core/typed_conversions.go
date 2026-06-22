// Package core provides type-safe conversions between LLM provider formats.
//
// All provider-specific formats convert to/from a canonical TypedMessage
// representation rather than passing map[string]interface{} through business logic.
//
// Content union types (OpenAIContentUnion, AnthropicContentUnion) model the
// polymorphic content field (either a string or an array of content objects).
// They implement UnmarshalJSON for parsing responses but deliberately fail
// MarshalJSON, so they must never be marshaled directly. Build request bodies
// with the dedicated marshal functions, which render the union types via ToMap():
//   - MarshalAnthropicMessagesForRequest
//   - MarshalOpenAIMessagesForRequest
//   - MarshalGoogleMessagesForRequest
package core
