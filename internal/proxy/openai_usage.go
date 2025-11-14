// Package proxy provides HTTP proxy functionality for LLM providers.
package proxy

import "lmtools/internal/constants"

// OpenAI Usage Handling
//
// This file contains OpenAI-specific usage tracking and streaming functionality.
// OpenAI's streaming API includes usage information in the final chunk when
// the include_usage flag is set, unlike other providers that include it
// throughout the stream.

// includeUsageFromMetadata determines if usage information should be included in the OpenAI stream.
// OpenAI's include_usage flag is passed through Anthropic's metadata field as
// constants.IncludeUsageKey to maintain API compatibility while using
// Anthropic's request format internally.
//
// This function is specifically for OpenAI streaming behavior and should only
// be used in OpenAI-related streaming code paths.
func includeUsageFromMetadata(anthReq *AnthropicRequest) bool {
	if anthReq == nil || anthReq.Metadata == nil {
		return false
	}
	v, _ := anthReq.Metadata[constants.IncludeUsageKey].(bool)
	return v
}
