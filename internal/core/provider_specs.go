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
	"sync"
)

type (
	providerChatBuilder    func(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error)
	providerEmbedBuilder   func(cfg RequestOptions, input string) (*http.Request, []byte, error)
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

var (
	providerSpecs     map[string]providerSpec
	providerSpecsOnce sync.Once
)

func initProviderSpecs() {
	providerSpecs = map[string]providerSpec{
		constants.ProviderOpenAI: {
			Provider: constants.ProviderOpenAI,
			BuildChat: func(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
				if useOpenAIResponses(cfg) {
					return buildOpenAIResponsesChatRequest(cfg, typedMessages, model, system, systemExplicit, toolDefs, toolChoice, stream)
				}
				return buildToolAwareRequest(cfg, constants.ProviderOpenAI, typedMessages, model, "", false, toolDefs, toolChoice, stream)
			},
			BuildEmbed:     buildOpenAIEmbedRequest,
			HandleStream:   handleOpenAIStreamWithTools,
			ParseResponse:  parseOpenAIResponse,
			ConvertTools:   convertOpenAITools,
			RequestMap:     openAIRequestMap,
			ResolveChatURL: openAIChatURL,
			Marshal:        marshalOpenAITypedMessages,
		},
		constants.ProviderAnthropic: {
			Provider: constants.ProviderAnthropic,
			BuildChat: func(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
				return buildToolAwareRequest(cfg, constants.ProviderAnthropic, typedMessages, model, system, systemExplicit, toolDefs, toolChoice, stream)
			},
			HandleStream:   handleAnthropicStreamWithTools,
			ParseResponse:  parseAnthropicResponse,
			ConvertTools:   convertAnthropicTools,
			RequestMap:     anthropicRequestMap,
			ResolveChatURL: anthropicChatURL,
			Marshal:        marshalAnthropicTypedMessages,
		},
		constants.ProviderGoogle: {
			Provider: constants.ProviderGoogle,
			BuildChat: func(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
				return buildToolAwareRequest(cfg, constants.ProviderGoogle, typedMessages, model, system, systemExplicit, toolDefs, toolChoice, stream)
			},
			HandleStream:   handleGoogleStreamWithTools,
			ParseResponse:  parseGoogleResponseDetailed,
			ConvertTools:   convertGoogleTools,
			RequestMap:     googleRequestMap,
			ResolveChatURL: googleChatURL,
			Marshal:        marshalGoogleTypedMessages,
		},
		constants.ProviderArgo: {
			Provider: constants.ProviderArgo,
			BuildChat: func(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
				return buildArgoChatRequest(cfg, typedMessages, model, system, systemExplicit, toolDefs, toolChoice, stream)
			},
			BuildEmbed: buildArgoEmbedRequest,
			HandleStream: func(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, _ Notifier) (Response, error) {
				return handleArgoStream(ctx, body, logFile, out)
			},
			ParseResponse: parseArgoResponse,
		},
	}
}

func providerSpecRegistry() map[string]providerSpec {
	providerSpecsOnce.Do(initProviderSpecs)
	return providerSpecs
}

func providerSpecForName(provider string) (providerSpec, error) {
	if spec, ok := providerSpecRegistry()[constants.NormalizeProvider(provider)]; ok {
		return spec, nil
	}
	return providerSpec{}, fmt.Errorf("unsupported provider: %s", provider)
}

func providerSpecForModel(model string) providerSpec {
	spec, err := providerSpecForName(providers.DetermineArgoModelProvider(model))
	if err != nil {
		// DetermineArgoModelProvider defaults unknown models to openai, so this is defensive.
		return providerSpecRegistry()[constants.ProviderOpenAI]
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
