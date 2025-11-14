package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"net/http"
	"strings"
)

// handleAPIKeyAuth handles API key authentication for all providers
func handleAPIKeyAuth(httpReq *http.Request, cfg RequestConfig, provider string) error {
	if keyFile := cfg.GetAPIKeyFile(); keyFile != "" {
		apiKey, err := auth.ReadKeyFile(keyFile)
		if err != nil {
			return fmt.Errorf("failed to read API key file: %w", err)
		}
		if apiKey == "" {
			return fmt.Errorf("API key file exists but contains empty key")
		}
		auth.SetProviderHeaders(httpReq, provider, apiKey)
	}
	return nil
}

// applyProviderAuth applies authentication based on the provider type
func applyProviderAuth(req *http.Request, cfg RequestConfig, provider string) error {
	if provider == constants.ProviderGoogle {
		// Google requires special handling with API key in URL
		if keyFile := cfg.GetAPIKeyFile(); keyFile != "" {
			apiKey, err := auth.ReadKeyFile(keyFile)
			if err != nil {
				return fmt.Errorf("failed to read Google API key: %w", err)
			}
			return auth.ApplyGoogleAPIKey(req, apiKey)
		}
	} else {
		// Standard header-based auth for other providers
		return handleAPIKeyAuth(req, cfg, provider)
	}
	return nil
}

// buildProviderRequest builds a unified provider request with common logic extracted
func buildProviderRequest(cfg RequestConfig, url string, body []byte, provider string, stream bool) (*http.Request, []byte, error) {
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	if err := applyProviderAuth(httpReq, cfg, provider); err != nil {
		return nil, nil, err
	}
	auth.SetRequestHeaders(httpReq, true, stream, provider)

	return httpReq, body, nil
}

// buildOpenAIToolAwareRequest builds an OpenAI-compatible request with tool support
func buildOpenAIToolAwareRequest(cfg RequestConfig, typedMessages []TypedMessage, model string, toolDefs []ToolDefinition, _ *ToolChoice, stream bool) (*http.Request, []byte, error) {
	// Convert messages to OpenAI format using typed conversion and helper
	typedOpenAIMessages := ToOpenAITyped(typedMessages)
	openAIMessages := MarshalOpenAIMessagesForRequest(typedOpenAIMessages)

	// Build OpenAI request with tool support
	reqMap := map[string]interface{}{
		"model":    model,
		"messages": openAIMessages,
		"stream":   stream,
	}

	// Add tools if enabled
	if len(toolDefs) > 0 {
		// Use typed definitions and convert them
		converted := ConvertToolsForProvider(model, toolDefs, nil)
		if converted.Tools != nil {
			reqMap["tools"] = converted.Tools
		}
		if converted.ToolChoice != nil {
			reqMap["tool_choice"] = converted.ToolChoice
		}
	}

	body, err := json.Marshal(reqMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://api.openai.com/v1"
	}
	url = strings.TrimRight(url, "/") + "/chat/completions"

	return buildProviderRequest(cfg, url, body, constants.ProviderOpenAI, stream)
}

// buildAnthropicToolAwareRequest builds an Anthropic-compatible request with tool support
func buildAnthropicToolAwareRequest(cfg RequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, _ *ToolChoice, stream bool) (*http.Request, []byte, error) {
	// Convert messages to Anthropic format using typed conversion and helper
	typedAnthropicMessages := ToAnthropicTyped(typedMessages)
	anthropicMessages := MarshalAnthropicMessagesForRequest(typedAnthropicMessages)

	// Build Anthropic request with tool support
	reqMap := map[string]interface{}{
		"model":    model,
		"messages": anthropicMessages,
		"stream":   stream,
	}

	// Add system message if present
	if system != "" {
		reqMap["system"] = system
	}

	// Add tools if enabled
	if len(toolDefs) > 0 {
		// Use typed definitions and convert them
		converted := ConvertToolsForProvider(model, toolDefs, nil)
		if converted.Tools != nil {
			reqMap["tools"] = converted.Tools
		}
		if converted.ToolChoice != nil {
			reqMap["tool_choice"] = converted.ToolChoice
		}
	}

	body, err := json.Marshal(reqMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Anthropic request: %w", err)
	}

	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://api.anthropic.com/v1"
	}
	url = strings.TrimRight(url, "/") + "/messages"

	return buildProviderRequest(cfg, url, body, constants.ProviderAnthropic, stream)
}

// buildGoogleToolAwareRequest builds a Google-compatible request with tool support
func buildGoogleToolAwareRequest(cfg RequestConfig, typedMessages []TypedMessage, model string, toolDefs []ToolDefinition, _ *ToolChoice, stream bool) (*http.Request, []byte, error) {
	// Convert messages to Google format using typed conversion
	typedGoogleMessages := ToGoogleTyped(typedMessages)

	// Convert typed messages to []interface{} using the centralized marshaling function
	googleMessages := MarshalGoogleMessagesForRequest(typedGoogleMessages)

	// Build Google request - note: Google has different structure
	reqMap := map[string]interface{}{
		"contents": googleMessages,
	}

	// Add system instruction if provided
	system := cfg.GetEffectiveSystem()
	if system != "" {
		reqMap["systemInstruction"] = map[string]interface{}{
			"parts": []map[string]string{
				{"text": system},
			},
		}
	}

	// Add tools if enabled using ConvertToolsForProvider
	if len(toolDefs) > 0 {
		// Use ConvertToolsForProvider to get properly formatted Google tools
		converted := ConvertToolsForProvider(model, toolDefs, nil)
		if converted.Tools != nil {
			// converted.Tools contains []GoogleTool which already has the right structure
			reqMap["tools"] = converted.Tools
		}

		// Enable function calling
		reqMap["toolConfig"] = map[string]interface{}{
			"functionCallConfig": map[string]interface{}{
				"mode": "AUTO",
			},
		}
	}

	body, err := json.Marshal(reqMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Google request: %w", err)
	}

	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://generativelanguage.googleapis.com/v1beta"
	}

	// Use streaming or non-streaming endpoint based on config
	if stream {
		url = fmt.Sprintf("%s/models/%s:streamGenerateContent", strings.TrimRight(url, "/"), model)
	} else {
		url = fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(url, "/"), model)
	}

	return buildProviderRequest(cfg, url, body, constants.ProviderGoogle, stream)
}

// buildChatRequestFromTyped builds a unified chat request from typed messages
// This function handles both initial requests and tool result follow-ups
func buildChatRequestFromTyped(cfg RequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, _ *ToolChoice, stream bool) (*http.Request, []byte, error) {
	if model == "" {
		return nil, nil, fmt.Errorf("model is required for building request")
	}

	// Validate that we have messages
	if len(typedMessages) == 0 {
		return nil, nil, fmt.Errorf("no messages provided for request")
	}

	provider := getProviderWithDefault(cfg, constants.ProviderArgo)

	switch provider {
	case constants.ProviderArgo:
		return buildArgoChatRequestTyped(cfg, typedMessages, stream)
	case constants.ProviderOpenAI:
		return buildOpenAIToolAwareRequest(cfg, typedMessages, model, toolDefs, nil, stream)
	case constants.ProviderAnthropic:
		// Extract system from messages for Anthropic
		sys, msgs := splitSystem(typedMessages)
		if sys == "" {
			sys = system // Use passed system if no message system
		}
		return buildAnthropicToolAwareRequest(cfg, msgs, model, sys, toolDefs, nil, stream)
	case constants.ProviderGoogle:
		return buildGoogleToolAwareRequest(cfg, typedMessages, model, toolDefs, nil, stream)
	default:
		return nil, nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}
