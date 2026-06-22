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
func handleAPIKeyAuth(httpReq *http.Request, cfg RequestOptions, provider string) error {
	if keyFile := cfg.APIKeyFile; keyFile != "" {
		providerKey, err := auth.LoadProviderKeyFile(provider, keyFile)
		if err != nil {
			return fmt.Errorf("failed to read API key file: %w", err)
		}
		auth.SetProviderHeaders(httpReq, providerKey.Provider, providerKey.Value)
	}
	return nil
}

func isArgoNativeWireProvider(cfg RequestOptions, provider string) bool {
	if getProviderWithDefault(cfg, constants.ProviderArgo) != constants.ProviderArgo {
		return false
	}
	return provider == constants.ProviderOpenAI || provider == constants.ProviderAnthropic
}

// applyProviderAuth applies authentication based on the provider type
func applyProviderAuth(req *http.Request, cfg RequestOptions, provider string) error {
	if isArgoNativeWireProvider(cfg, provider) {
		if cfg.APIKeyFile != "" {
			return handleAPIKeyAuth(req, cfg, provider)
		}
		if cfg.User != "" {
			auth.SetProviderHeaders(req, provider, cfg.User)
		}
		return nil
	}
	switch providers.CredentialKindFor(provider) {
	case providers.CredentialKindAPIKey:
		return handleAPIKeyAuth(req, cfg, provider)
	case providers.CredentialKindArgoUser, providers.CredentialKindNone:
		return nil
	}
	return nil
}

// buildProviderRequest builds a unified provider request with common logic extracted
func buildProviderRequest(cfg RequestOptions, url string, body []byte, provider string, stream bool) (*http.Request, []byte, error) {
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

func buildToolAwareRequest(cfg RequestOptions, provider string, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	spec, err := requireProviderRequestSpec(provider)
	if err != nil {
		return nil, nil, err
	}

	payload, err := PrepareRequestPayloadWithSystemExplicit(provider, model, typedMessages, system, systemExplicit, toolDefs, toolChoice, stream)
	if err != nil {
		return nil, nil, err
	}
	applyOutputOptionsFromConfig(&payload, cfg)

	body, err := json.Marshal(spec.RequestMap(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal %s request: %w", provider, err)
	}

	endpoint, err := providers.ResolveChatURLWithArgoOptions(provider, cfg.ProviderURL, cfg.Env, model, stream, false)
	if err != nil {
		return nil, nil, err
	}
	return buildProviderRequest(cfg, endpoint, body, provider, stream)
}

// buildChatRequestFromTyped builds a unified chat request from typed messages
// This function handles both initial requests and tool result follow-ups
func buildChatRequestFromTyped(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
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

	return buildChat(cfg, typedMessages, model, system, systemExplicit, toolDefs, toolChoice, stream)
}
