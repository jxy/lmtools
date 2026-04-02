package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
)

type (
	providerRequestMapper     func(PreparedRequestPayload) map[string]interface{}
	providerChatURLResolver   func(cfg ProviderConfig, model string, stream bool) string
	providerMessageMarshaller func(messages []TypedMessage, keepGoogleSystem bool) []interface{}
)

func requireProviderRequestSpec(provider string) (providerSpec, error) {
	spec, err := providerSpecForName(provider)
	if err != nil {
		return providerSpec{}, err
	}
	if !spec.supportsGenericRequest() {
		return providerSpec{}, fmt.Errorf("provider %s does not support generic request building", provider)
	}
	return spec, nil
}

func addToolFields(reqMap map[string]interface{}, payload PreparedRequestPayload) {
	if payload.Tools != nil {
		reqMap["tools"] = payload.Tools
	}
	if payload.ToolChoice != nil {
		reqMap["tool_choice"] = payload.ToolChoice
	}
}

func marshalArgoTypedMessages(model string, messages []TypedMessage) []interface{} {
	spec := providerSpecForModel(model)
	if spec.Marshal == nil {
		return marshalOpenAITypedMessages(messages, true)
	}
	return spec.Marshal(messages, true)
}

// handleAPIKeyAuth handles API key authentication for all providers
func handleAPIKeyAuth(httpReq *http.Request, cfg ProviderConfig, provider string) error {
	if keyFile := cfg.GetAPIKeyFile(); keyFile != "" {
		apiKey, err := auth.ReadKeyFile(keyFile)
		if err != nil {
			return fmt.Errorf("failed to read API key file: %w", err)
		}
		if apiKey == "" {
			return fmt.Errorf("API key file exists but contains empty key")
		}
		if err := auth.ApplyProviderCredentials(httpReq, provider, apiKey); err != nil {
			return err
		}
	}
	return nil
}

// applyProviderAuth applies authentication based on the provider type
func applyProviderAuth(req *http.Request, cfg ProviderConfig, provider string) error {
	switch providers.CredentialKindFor(provider) {
	case providers.CredentialKindAPIKey:
		return handleAPIKeyAuth(req, cfg, provider)
	case providers.CredentialKindArgoUser, providers.CredentialKindNone:
		return nil
	}
	return nil
}

// buildProviderRequest builds a unified provider request with common logic extracted
func buildProviderRequest(cfg ProviderConfig, url string, body []byte, provider string, stream bool) (*http.Request, []byte, error) {
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

func buildToolAwareRequest(cfg ChatRequestConfig, provider string, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	spec, err := requireProviderRequestSpec(provider)
	if err != nil {
		return nil, nil, err
	}

	payload, err := PrepareRequestPayload(provider, model, typedMessages, system, toolDefs, toolChoice, stream)
	if err != nil {
		return nil, nil, err
	}

	body, err := json.Marshal(spec.RequestMap(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal %s request: %w", provider, err)
	}

	return buildProviderRequest(cfg, spec.ResolveChatURL(cfg, model, stream), body, provider, stream)
}

// buildChatRequestFromTyped builds a unified chat request from typed messages
// This function handles both initial requests and tool result follow-ups
func buildChatRequestFromTyped(cfg ChatRequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	if model == "" {
		return nil, nil, fmt.Errorf("model is required for building request")
	}

	// Validate that we have messages
	if len(typedMessages) == 0 {
		return nil, nil, fmt.Errorf("no messages provided for request")
	}

	provider := getProviderWithDefault(cfg, constants.ProviderArgo)
	spec, err := providerSpecForName(provider)
	if err != nil {
		return nil, nil, err
	}
	buildChat, err := spec.requireChatBuilder()
	if err != nil {
		return nil, nil, err
	}

	return buildChat(cfg, typedMessages, model, system, toolDefs, toolChoice, stream)
}
