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

type chatRequestPayload struct {
	Provider   string
	Model      string
	Messages   []TypedMessage
	System     string
	Tools      interface{}
	ToolChoice interface{}
	Stream     bool
}

func newChatRequestPayload(provider, model string, typedMessages []TypedMessage, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) chatRequestPayload {
	payload := chatRequestPayload{
		Provider: provider,
		Model:    model,
		Messages: typedMessages,
		System:   system,
		Stream:   stream,
	}

	if provider == constants.ProviderAnthropic || provider == constants.ProviderGoogle {
		inlineSystem, rest := splitSystem(typedMessages)
		if payload.System == "" {
			payload.System = inlineSystem
		}
		payload.Messages = rest
	}

	if len(toolDefs) > 0 {
		converted := ConvertToolsForProvider(model, toolDefs, toolChoice)
		payload.Tools = converted.Tools
		payload.ToolChoice = converted.ToolChoice
	}

	return payload
}

func (p chatRequestPayload) requestMap() map[string]interface{} {
	switch p.Provider {
	case constants.ProviderAnthropic:
		reqMap := map[string]interface{}{
			"model":    p.Model,
			"messages": marshalTypedMessagesForProvider(constants.ProviderAnthropic, p.Messages, false),
			"stream":   p.Stream,
		}
		if p.System != "" {
			reqMap["system"] = p.System
		}
		addToolFields(reqMap, p)
		return reqMap

	case constants.ProviderGoogle:
		reqMap := map[string]interface{}{
			"contents": marshalTypedMessagesForProvider(constants.ProviderGoogle, p.Messages, false),
		}
		if p.System != "" {
			reqMap["systemInstruction"] = googleSystemInstruction(p.System)
		}
		if p.Tools != nil {
			reqMap["tools"] = p.Tools
			reqMap["toolConfig"] = googleAutoToolConfig()
		}
		return reqMap

	case constants.ProviderOpenAI:
		fallthrough
	default:
		reqMap := map[string]interface{}{
			"model":    p.Model,
			"messages": marshalTypedMessagesForProvider(constants.ProviderOpenAI, p.Messages, false),
			"stream":   p.Stream,
		}
		addToolFields(reqMap, p)
		return reqMap
	}
}

func addToolFields(reqMap map[string]interface{}, payload chatRequestPayload) {
	if payload.Tools != nil {
		reqMap["tools"] = payload.Tools
	}
	if payload.ToolChoice != nil {
		reqMap["tool_choice"] = payload.ToolChoice
	}
}

func googleSystemInstruction(system string) map[string]interface{} {
	return map[string]interface{}{
		"parts": []map[string]string{
			{"text": system},
		},
	}
}

func googleAutoToolConfig() map[string]interface{} {
	return map[string]interface{}{
		"functionCallConfig": map[string]interface{}{
			"mode": "AUTO",
		},
	}
}

func resolveProviderChatURL(cfg RequestConfig, provider, model string, stream bool) string {
	url := cfg.GetProviderURL()

	switch provider {
	case constants.ProviderAnthropic:
		if url == "" {
			url = "https://api.anthropic.com/v1"
		}
		return strings.TrimRight(url, "/") + "/messages"

	case constants.ProviderGoogle:
		if url == "" {
			url = "https://generativelanguage.googleapis.com/v1beta"
		}
		if stream {
			return fmt.Sprintf("%s/models/%s:streamGenerateContent", strings.TrimRight(url, "/"), model)
		}
		return fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(url, "/"), model)

	case constants.ProviderOpenAI:
		fallthrough
	default:
		if url == "" {
			url = "https://api.openai.com/v1"
		}
		return strings.TrimRight(url, "/") + "/chat/completions"
	}
}

func marshalTypedMessagesForProvider(provider string, messages []TypedMessage, keepGoogleSystem bool) []interface{} {
	switch provider {
	case constants.ProviderAnthropic:
		return MarshalAnthropicMessagesForRequest(ToAnthropicTyped(messages))
	case constants.ProviderGoogle:
		if keepGoogleSystem {
			return MarshalGoogleMessagesForRequest(ToGoogleForArgoTyped(messages))
		}
		return MarshalGoogleMessagesForRequest(ToGoogleTyped(messages))
	case constants.ProviderOpenAI:
		fallthrough
	default:
		return MarshalOpenAIMessagesForRequest(ToOpenAITyped(messages))
	}
}

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
func buildOpenAIToolAwareRequest(cfg RequestConfig, typedMessages []TypedMessage, model string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	payload := newChatRequestPayload(constants.ProviderOpenAI, model, typedMessages, "", toolDefs, toolChoice, stream)

	body, err := json.Marshal(payload.requestMap())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	return buildProviderRequest(cfg, resolveProviderChatURL(cfg, constants.ProviderOpenAI, model, stream), body, constants.ProviderOpenAI, stream)
}

// buildAnthropicToolAwareRequest builds an Anthropic-compatible request with tool support
func buildAnthropicToolAwareRequest(cfg RequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	payload := newChatRequestPayload(constants.ProviderAnthropic, model, typedMessages, system, toolDefs, toolChoice, stream)

	body, err := json.Marshal(payload.requestMap())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Anthropic request: %w", err)
	}

	return buildProviderRequest(cfg, resolveProviderChatURL(cfg, constants.ProviderAnthropic, model, stream), body, constants.ProviderAnthropic, stream)
}

// buildGoogleToolAwareRequest builds a Google-compatible request with tool support
func buildGoogleToolAwareRequest(cfg RequestConfig, typedMessages []TypedMessage, model string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	payload := newChatRequestPayload(constants.ProviderGoogle, model, typedMessages, cfg.GetEffectiveSystem(), toolDefs, toolChoice, stream)

	body, err := json.Marshal(payload.requestMap())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Google request: %w", err)
	}

	return buildProviderRequest(cfg, resolveProviderChatURL(cfg, constants.ProviderGoogle, model, stream), body, constants.ProviderGoogle, stream)
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
		sys, _ := splitSystem(typedMessages)
		if sys == "" {
			sys = system
		}
		return buildAnthropicToolAwareRequest(cfg, typedMessages, model, sys, toolDefs, nil, stream)
	case constants.ProviderGoogle:
		return buildGoogleToolAwareRequest(cfg, typedMessages, model, toolDefs, nil, stream)
	default:
		return nil, nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}
