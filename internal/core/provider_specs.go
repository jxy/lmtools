package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
	"os"
)

type (
	providerChatBuilder    func(cfg ChatRequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error)
	providerEmbedBuilder   func(cfg EmbedRequestConfig, input string) (*http.Request, []byte, error)
	providerStreamHandler  func(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (Response, error)
	providerResponseParser func(data []byte, isEmbed bool) (Response, error)
	providerToolConverter  func(tools []ToolDefinition, toolChoice *ToolChoice) ConvertedTools
)

type providerSpec struct {
	Provider       string
	BuildChat      providerChatBuilder
	BuildEmbed     providerEmbedBuilder
	HandleStream   providerStreamHandler
	ParseResponse  providerResponseParser
	ConvertTools   providerToolConverter
	RequestMap     providerRequestMapper
	ResolveChatURL providerChatURLResolver
	Marshal        providerMessageMarshaller
}

func openAIProviderSpec() providerSpec {
	return providerSpec{
		Provider: constants.ProviderOpenAI,
		BuildChat: func(cfg ChatRequestConfig, typedMessages []TypedMessage, model string, _ string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
			return buildToolAwareRequest(cfg, constants.ProviderOpenAI, typedMessages, model, "", toolDefs, toolChoice, stream)
		},
		BuildEmbed:     buildOpenAIEmbedRequest,
		HandleStream:   handleOpenAIStreamWithTools,
		ParseResponse:  parseOpenAIResponse,
		ConvertTools:   convertOpenAITools,
		RequestMap:     openAIRequestMap,
		ResolveChatURL: openAIChatURL,
		Marshal:        marshalOpenAITypedMessages,
	}
}

func anthropicProviderSpec() providerSpec {
	return providerSpec{
		Provider: constants.ProviderAnthropic,
		BuildChat: func(cfg ChatRequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
			return buildToolAwareRequest(cfg, constants.ProviderAnthropic, typedMessages, model, system, toolDefs, toolChoice, stream)
		},
		HandleStream:   handleAnthropicStreamWithTools,
		ParseResponse:  parseAnthropicResponse,
		ConvertTools:   convertAnthropicTools,
		RequestMap:     anthropicRequestMap,
		ResolveChatURL: anthropicChatURL,
		Marshal:        marshalAnthropicTypedMessages,
	}
}

func googleProviderSpec() providerSpec {
	return providerSpec{
		Provider: constants.ProviderGoogle,
		BuildChat: func(cfg ChatRequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
			return buildToolAwareRequest(cfg, constants.ProviderGoogle, typedMessages, model, system, toolDefs, toolChoice, stream)
		},
		HandleStream:   handleGoogleStreamWithTools,
		ParseResponse:  parseGoogleResponseDetailed,
		ConvertTools:   convertGoogleTools,
		RequestMap:     googleRequestMap,
		ResolveChatURL: googleChatURL,
		Marshal:        marshalGoogleTypedMessages,
	}
}

func argoProviderSpec() providerSpec {
	return providerSpec{
		Provider: constants.ProviderArgo,
		BuildChat: func(cfg ChatRequestConfig, typedMessages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
			return buildArgoChatRequest(cfg, typedMessages, model, system, toolDefs, toolChoice, stream)
		},
		BuildEmbed: buildArgoEmbedRequest,
		HandleStream: func(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, _ Notifier) (Response, error) {
			return handleArgoStream(ctx, body, logFile, out)
		},
		ParseResponse: parseArgoResponse,
	}
}

func defaultOpenAIProviderSpec() providerSpec {
	return openAIProviderSpec()
}

func providerSpecForName(provider string) (providerSpec, error) {
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI:
		return openAIProviderSpec(), nil
	case constants.ProviderAnthropic:
		return anthropicProviderSpec(), nil
	case constants.ProviderGoogle:
		return googleProviderSpec(), nil
	case constants.ProviderArgo:
		return argoProviderSpec(), nil
	}
	return providerSpec{}, fmt.Errorf("unsupported provider: %s", provider)
}

func providerSpecForModel(model string) providerSpec {
	spec, err := providerSpecForName(providers.DetermineArgoModelProvider(model))
	if err != nil {
		// DetermineArgoModelProvider defaults unknown models to openai, so this is defensive.
		return defaultOpenAIProviderSpec()
	}
	return spec
}

func unknownProviderSpec(provider string) providerSpec {
	return providerSpec{Provider: constants.NormalizeProvider(provider)}
}

func (spec providerSpec) displayName() string {
	if info, ok := providers.InfoFor(spec.Provider); ok {
		return info.DisplayName
	}
	if spec.Provider != "" {
		return spec.Provider
	}
	return "unknown"
}

func (spec providerSpec) supportsGenericRequest() bool {
	return spec.RequestMap != nil && spec.ResolveChatURL != nil && spec.Marshal != nil
}

func (spec providerSpec) usesOutOfBandSystemPrompt() bool {
	return providers.UsesOutOfBandSystemPrompt(spec.Provider)
}

func (spec providerSpec) supportsEmbeddings() bool {
	return providers.SupportsEmbeddings(spec.Provider)
}

func (spec providerSpec) requireChatBuilder() (providerChatBuilder, error) {
	if spec.BuildChat == nil {
		return nil, fmt.Errorf("unsupported provider: %s", spec.Provider)
	}
	return spec.BuildChat, nil
}

func (spec providerSpec) requireEmbedBuilder() (providerEmbedBuilder, error) {
	if spec.BuildEmbed == nil {
		return nil, fmt.Errorf("%s provider does not support embedding mode", spec.displayName())
	}
	return spec.BuildEmbed, nil
}

func (spec providerSpec) parseResponseData(data []byte, isEmbed bool) (Response, error) {
	if spec.ParseResponse == nil {
		return Response{}, fmt.Errorf("unsupported provider: %s", spec.Provider)
	}
	return spec.ParseResponse(data, isEmbed)
}

func (spec providerSpec) handleStreamResponse(ctx context.Context, body io.ReadCloser, logger Logger, notifier Notifier) (Response, error) {
	f, path, err := logger.CreateLogFile(logger.GetLogDir(), "stream_chat_output")
	if err != nil {
		return Response{}, fmt.Errorf("failed to create log file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			notifier.Warnf("Failed to close log file %s: %v", path, closeErr)
		}
	}()

	if spec.HandleStream != nil {
		return spec.HandleStream(ctx, body, f, os.Stdout, notifier)
	}

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.MultiWriter(os.Stdout, f, &buf), body)
		done <- err
	}()

	select {
	case <-ctx.Done():
		return Response{Text: buf.String()}, fmt.Errorf("streaming interrupted: %w", ctx.Err())
	case err := <-done:
		if err != nil {
			return Response{Text: buf.String()}, fmt.Errorf("error streaming response: %w", err)
		}
		return Response{Text: buf.String()}, nil
	}
}

func (spec providerSpec) convertToolsForRequest(tools []ToolDefinition, toolChoice *ToolChoice) ConvertedTools {
	if len(tools) == 0 || spec.ConvertTools == nil {
		return ConvertedTools{}
	}
	return spec.ConvertTools(tools, toolChoice)
}
