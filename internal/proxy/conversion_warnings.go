package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
)

func warnDroppedField(ctx context.Context, source, target, field, reason string) {
	if reason == "" {
		reason = "no equivalent target-provider field"
	}
	logger.From(ctx).Warnf("Dropping %s field %q while converting to %s: %s", source, field, target, reason)
}

func warnConvertedFieldLoss(ctx context.Context, source, target, field, reason string) {
	logger.From(ctx).Warnf("Converting %s field %q to %s with limited fidelity: %s", source, field, target, reason)
}

func warnDroppedResponsesTool(ctx context.Context, index int, toolType string) {
	if ctx == nil {
		return
	}
	logger.From(ctx).Warnf("Dropping unsupported Responses tool type %q at index %d; only function and custom tools are converted by compatibility provider paths", toolType, index)
}

func warnDroppedResponsesToolChoice(ctx context.Context, choiceType string) {
	if ctx == nil {
		return
	}
	logger.From(ctx).Warnf("Dropping unsupported Responses tool_choice type %q; only function and custom tool choices are converted by compatibility provider paths", choiceType)
}

func warnOpenAIRequestDropsForAnthropic(ctx context.Context, req *OpenAIRequest) {
	if req == nil {
		return
	}
	if req.N != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "n", "Anthropic returns one message per request")
	}
	if req.PresencePenalty != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "presence_penalty", "")
	}
	if req.FrequencyPenalty != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "frequency_penalty", "")
	}
	if len(req.LogitBias) > 0 {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "logit_bias", "")
	}
	if req.Store != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "store", "")
	}
	if req.Seed != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "seed", "")
	}
	if req.Verbosity != "" {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "verbosity", "")
	}
	if len(req.Modalities) > 0 {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "modalities", "Anthropic Messages has no matching output modality selector")
	}
	if req.Audio != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "audio", "output audio is OpenAI-specific")
	}
	if req.Prediction != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "prediction", "")
	}
	if req.WebSearchOptions != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "web_search_options", "Anthropic web search is configured as a server tool")
	}
	if req.PromptCacheKey != "" {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "prompt_cache_key", "Anthropic prompt caching is block-scoped cache_control")
	}
	if req.PromptCacheRetention != "" {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "prompt_cache_retention", "Anthropic prompt caching is block-scoped cache_control")
	}
	if req.SafetyIdentifier != "" && req.User != "" {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "safety_identifier", "user already maps to Anthropic metadata.user_id")
	}
	if req.ParallelToolCalls != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "parallel_tool_calls", "")
	}
	if req.Logprobs != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "logprobs", "")
	}
	if req.TopLogprobs != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "top_logprobs", "")
	}
	if len(req.ExtraBody) > 0 {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "extra_body", "")
	}
	if req.Temperature != nil && anthropicModelRejectsTemperature(req.Model) {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "temperature", "Anthropic rejects temperature for Claude Opus 4.7")
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeObfuscation != nil {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "stream_options.include_obfuscation", "")
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "" && openAIResponseFormatToAnthropicFormat(req.ResponseFormat) == nil && req.ResponseFormat.Type != "text" {
		warnDroppedField(ctx, "OpenAI", "Anthropic", "response_format", "unsupported response_format type")
	}
	if req.ResponseFormat != nil && strings.EqualFold(strings.TrimSpace(req.ResponseFormat.Type), "json_schema") && req.ResponseFormat.JSONSchema != nil {
		if req.ResponseFormat.JSONSchema.Name != "" {
			warnConvertedFieldLoss(ctx, "OpenAI", "Anthropic", "response_format.json_schema.name", "Anthropic output_config.format does not carry schema names")
		}
		if req.ResponseFormat.JSONSchema.Description != "" {
			warnConvertedFieldLoss(ctx, "OpenAI", "Anthropic", "response_format.json_schema.description", "Anthropic output_config.format does not accept a top-level schema description")
		}
		if req.ResponseFormat.JSONSchema.Strict != nil {
			warnConvertedFieldLoss(ctx, "OpenAI", "Anthropic", "response_format.json_schema.strict", "Anthropic JSON outputs are schema constrained but do not accept OpenAI's strict flag")
		}
	}
	switch strings.ToLower(strings.TrimSpace(req.ServiceTier)) {
	case "default":
		warnConvertedFieldLoss(ctx, "OpenAI", "Anthropic", "service_tier", "OpenAI default maps to Anthropic standard_only")
	case "priority":
		warnConvertedFieldLoss(ctx, "OpenAI", "Anthropic", "service_tier", "Anthropic auto may use Priority Tier but can fall back to standard capacity")
	case "flex":
		warnDroppedField(ctx, "OpenAI", "Anthropic", "service_tier", "Anthropic has no flex service tier")
	default:
		if req.ServiceTier != "" && serviceTierForAnthropic(req.ServiceTier) == "" {
			warnDroppedField(ctx, "OpenAI", "Anthropic", "service_tier", "unsupported Anthropic service_tier value")
		}
	}
	for _, msg := range req.Messages {
		if msg.Name != "" {
			warnDroppedField(ctx, "OpenAI", "Anthropic", "messages[].name", "")
		}
		if msg.Refusal != nil {
			warnDroppedField(ctx, "OpenAI", "Anthropic", "messages[].refusal", "refusal annotations are response-only metadata")
		}
		if len(msg.Annotations) > 0 {
			warnDroppedField(ctx, "OpenAI", "Anthropic", "messages[].annotations", "annotations have no equivalent request field")
		}
		if msg.Audio != nil {
			warnDroppedField(ctx, "OpenAI", "Anthropic", "messages[].audio", "assistant audio references are OpenAI-specific")
		}
	}
	for _, tool := range req.Tools {
		switch tool.Type {
		case "", "function":
		case "custom":
			warnConvertedFieldLoss(ctx, "OpenAI", "Anthropic", "tools[].custom", "custom tool input is wrapped in an Anthropic object schema and grammar validation is not enforced upstream")
		default:
			warnDroppedField(ctx, "OpenAI", "Anthropic", "tools[]."+tool.Type, "only function and custom tools are converted")
		}
	}
	warnOpenAIInstructionRoleConversions(ctx, "Anthropic", req)
}

func warnOpenAIRequestDropsForGoogle(ctx context.Context, req *OpenAIRequest) {
	if req == nil {
		return
	}
	if req.N != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "n", "Google returns one candidate through this compatibility path")
	}
	if req.PresencePenalty != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "presence_penalty", "")
	}
	if req.FrequencyPenalty != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "frequency_penalty", "")
	}
	if len(req.LogitBias) > 0 {
		warnDroppedField(ctx, "OpenAI", "Google", "logit_bias", "")
	}
	if len(req.Metadata) > 0 {
		warnDroppedField(ctx, "OpenAI", "Google", "metadata", "")
	}
	if req.User != "" {
		warnDroppedField(ctx, "OpenAI", "Google", "user", "")
	}
	if req.SafetyIdentifier != "" {
		warnDroppedField(ctx, "OpenAI", "Google", "safety_identifier", "")
	}
	if req.ServiceTier != "" {
		warnDroppedField(ctx, "OpenAI", "Google", "service_tier", "")
	}
	if req.Store != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "store", "")
	}
	if req.Seed != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "seed", "")
	}
	if req.Verbosity != "" {
		warnDroppedField(ctx, "OpenAI", "Google", "verbosity", "")
	}
	if req.WebSearchOptions != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "web_search_options", "Gemini web grounding is configured with Google-native tools")
	}
	if len(req.Modalities) > 0 {
		warnDroppedField(ctx, "OpenAI", "Google", "modalities", "Google response modalities are not represented in this compatibility path")
	}
	if req.Audio != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "audio", "output audio is OpenAI-specific")
	}
	if req.Prediction != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "prediction", "")
	}
	if req.PromptCacheKey != "" {
		warnDroppedField(ctx, "OpenAI", "Google", "prompt_cache_key", "")
	}
	if req.PromptCacheRetention != "" {
		warnDroppedField(ctx, "OpenAI", "Google", "prompt_cache_retention", "")
	}
	if req.ParallelToolCalls != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "parallel_tool_calls", "")
	}
	if req.Logprobs != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "logprobs", "")
	}
	if req.TopLogprobs != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "top_logprobs", "")
	}
	if len(req.ExtraBody) > 0 {
		warnDroppedField(ctx, "OpenAI", "Google", "extra_body", "")
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeObfuscation != nil {
		warnDroppedField(ctx, "OpenAI", "Google", "stream_options.include_obfuscation", "")
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" && req.ResponseFormat.JSONSchema != nil && req.ResponseFormat.JSONSchema.Strict != nil && *req.ResponseFormat.JSONSchema.Strict {
		warnConvertedFieldLoss(ctx, "OpenAI", "Google", "response_format.json_schema.strict", "Google JSON schema mode does not expose the same strict flag")
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "" && req.ResponseFormat.Type != "text" && req.ResponseFormat.Type != "json_object" && req.ResponseFormat.Type != "json_schema" {
		warnDroppedField(ctx, "OpenAI", "Google", "response_format", "unsupported response_format type")
	}
	for _, msg := range req.Messages {
		if msg.Name != "" {
			warnDroppedField(ctx, "OpenAI", "Google", "messages[].name", "")
		}
		if msg.Refusal != nil {
			warnDroppedField(ctx, "OpenAI", "Google", "messages[].refusal", "refusal annotations are response-only metadata")
		}
		if len(msg.Annotations) > 0 {
			warnDroppedField(ctx, "OpenAI", "Google", "messages[].annotations", "annotations have no equivalent request field")
		}
		if msg.Audio != nil {
			warnDroppedField(ctx, "OpenAI", "Google", "messages[].audio", "assistant audio references are OpenAI-specific")
		}
	}
	for _, tool := range req.Tools {
		if tool.Type == "custom" {
			warnConvertedFieldLoss(ctx, "OpenAI", "Google", "tools[].custom", "custom tool input is wrapped in a Google function declaration and grammar validation is not enforced upstream")
			continue
		}
		if tool.Type != "" && tool.Type != "function" {
			warnDroppedField(ctx, "OpenAI", "Google", "tools[]."+tool.Type, "only function tools are converted")
		}
		if tool.Function.Strict != nil {
			warnConvertedFieldLoss(ctx, "OpenAI", "Google", "tools[].function.strict", "Google function declarations do not expose OpenAI strict tool enforcement")
		}
	}
	warnOpenAIInstructionRoleConversions(ctx, "Google", req)
}

func warnAnthropicRequestDropsForOpenAI(ctx context.Context, req *AnthropicRequest) {
	if req == nil {
		return
	}
	if req.TopK != nil {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "top_k", "")
	}
	if req.ContextManagement != nil {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "context_management", "")
	}
	if req.Container != "" {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "container", "")
	}
	if req.InferenceGeo != "" {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "inference_geo", "")
	}
	if req.Speed != "" {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "speed", "")
	}
	if req.CacheControl != nil {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "cache_control", "OpenAI prompt caching controls are top-level and not equivalent")
	}
	switch strings.ToLower(strings.TrimSpace(req.ServiceTier)) {
	case "standard_only", "standard":
		warnConvertedFieldLoss(ctx, "Anthropic", "OpenAI", "service_tier", "Anthropic standard capacity maps to OpenAI default service tier")
	default:
		if req.ServiceTier != "" && serviceTierForOpenAI(req.ServiceTier) == "" {
			warnDroppedField(ctx, "Anthropic", "OpenAI", "service_tier", "unsupported OpenAI service_tier value")
		}
	}
	if len(req.MCPServers) > 0 {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "mcp_servers", "")
	}
	if req.OutputConfig != nil && req.OutputConfig.Format != nil && anthropicOutputConfigToOpenAIResponseFormat(req.OutputConfig) == nil {
		warnDroppedField(ctx, "Anthropic", "OpenAI", "output_config.format", "unsupported output format shape")
	}
	for _, tool := range req.Tools {
		if tool.Type != "" && tool.Type != "custom" && tool.Type != "function" {
			warnDroppedField(ctx, "Anthropic", "OpenAI", "tools[]."+tool.Type, "only client/custom tools are converted")
		}
		if tool.CacheControl != nil {
			warnDroppedField(ctx, "Anthropic", "OpenAI", "tools[].cache_control", "")
		}
	}
	warnUnsupportedAnthropicBlocks(ctx, "OpenAI", req.Messages)
}

func warnAnthropicRequestDropsForGoogle(ctx context.Context, req *AnthropicRequest) {
	if req == nil {
		return
	}
	if len(req.Metadata) > 0 {
		warnDroppedField(ctx, "Anthropic", "Google", "metadata", "")
	}
	if req.ServiceTier != "" {
		warnDroppedField(ctx, "Anthropic", "Google", "service_tier", "")
	}
	if req.Container != "" {
		warnDroppedField(ctx, "Anthropic", "Google", "container", "")
	}
	if req.ContextManagement != nil {
		warnDroppedField(ctx, "Anthropic", "Google", "context_management", "")
	}
	if req.InferenceGeo != "" {
		warnDroppedField(ctx, "Anthropic", "Google", "inference_geo", "")
	}
	if req.Speed != "" {
		warnDroppedField(ctx, "Anthropic", "Google", "speed", "")
	}
	if req.CacheControl != nil {
		warnDroppedField(ctx, "Anthropic", "Google", "cache_control", "")
	}
	if len(req.MCPServers) > 0 {
		warnDroppedField(ctx, "Anthropic", "Google", "mcp_servers", "")
	}
	if req.OutputConfig != nil && req.OutputConfig.Format != nil {
		warnConvertedFieldLoss(ctx, "Anthropic", "Google", "output_config.format", "schema shape is mapped to Google JSON response configuration when possible")
	}
	for _, tool := range req.Tools {
		if tool.Type != "" && tool.Type != "custom" && tool.Type != "function" {
			warnDroppedField(ctx, "Anthropic", "Google", "tools[]."+tool.Type, "only client/custom tools are converted")
		}
		if tool.CacheControl != nil {
			warnDroppedField(ctx, "Anthropic", "Google", "tools[].cache_control", "")
		}
	}
	warnUnsupportedAnthropicBlocks(ctx, "Google", req.Messages)
}

func warnUnsupportedAnthropicBlocks(ctx context.Context, target string, messages []AnthropicMessage) {
	for _, msg := range messages {
		_, blocks, err := parseAnthropicMessageContent(msg.Content)
		if err != nil {
			continue
		}
		for _, block := range blocks {
			switch block.Type {
			case "text", "tool_use", "tool_result", "image", "audio", "input_audio", "file":
				if block.CacheControl != nil {
					warnDroppedField(ctx, "Anthropic", target, "content[].cache_control", "")
				}
				if len(block.Citations) > 0 {
					warnDroppedField(ctx, "Anthropic", target, "content[].citations", "")
				}
			case "thinking":
				warnDroppedField(ctx, "Anthropic", target, "content[].thinking", "thinking blocks are not part of target request messages")
			default:
				warnDroppedField(ctx, "Anthropic", target, "content[]."+block.Type, "unsupported content block type")
			}
		}
	}
}

func warnOpenAIInstructionRoleConversions(ctx context.Context, target string, req *OpenAIRequest) {
	if req == nil || len(req.Messages) == 0 {
		return
	}

	typed := OpenAIRequestToTyped(req)
	leading, rest := splitLeadingInstructionMessages(typed.Messages)
	if len(leading) > 1 {
		warnConvertedFieldLoss(ctx, "OpenAI", target, "messages[leading instruction prefix]", target+" merges leading system/developer messages into a single out-of-band system prompt")
	} else if len(leading) == 1 && leading[0].Role == string(core.RoleDeveloper) {
		warnConvertedFieldLoss(ctx, "OpenAI", target, "messages[0].role", target+" does not expose a separate developer role; converting it into the out-of-band system prompt")
	}

	for i, msg := range rest {
		if !isInstructionRole(msg.Role) {
			continue
		}
		reason := fmt.Sprintf("%s cannot represent mid-conversation %s messages; coercing the role to user to preserve array order", target, msg.Role)
		warnConvertedFieldLoss(ctx, "OpenAI", target, fmt.Sprintf("messages[%d].role", len(leading)+i), reason)
	}
}
