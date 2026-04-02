// Package proxy provides streaming functionality for various LLM providers.
//
// Usage Streaming Rules:
//
// Anthropic Format:
// - Usage is ALWAYS included in the message_delta event
// - The message_delta event contains both input_tokens and output_tokens
// - Usage appears BEFORE message_stop
// - No conditional inclusion based on request parameters
//
// OpenAI Format:
// - Usage is ONLY included when constants.IncludeUsageKey is true
// - Usage appears in a separate chunk AFTER the finish_reason chunk
// - Intermediate chunks have explicit "usage: null"
// - The usage chunk appears before the [DONE] marker
//
// Google Format:
// - Usage is included in usageMetadata for each response chunk
// - Responses contain prompt, candidate, and total token counts
//
// Implementation is split by concern across SSE writing, Anthropic handler state,
// parser helpers, provider parser adapters, and OpenAI writer/converter code.
package proxy
