package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"sync"
)

type (
	anthropicResponseForwarderFactory func(*Server) anthropicResponseForwarder
	anthropicStreamForwarderFactory   func(*Server) anthropicStreamForwarder
	openAIStreamForwarderFactory      func(*Server) openAIStreamForwarder
)

type proxyProviderCapability struct {
	Provider          string
	ParseModels       func(*Server, []byte) ([]ModelItem, error)
	RenderTyped       typedRequestRenderer
	AnthropicResponse anthropicResponseForwarderFactory
	AnthropicStream   anthropicStreamForwarderFactory
	OpenAIStream      openAIStreamForwarderFactory
}

var (
	proxyProviderCapabilities     map[string]proxyProviderCapability
	proxyProviderCapabilitiesOnce sync.Once
)

func initProxyProviderCapabilities() {
	proxyProviderCapabilities = map[string]proxyProviderCapability{
		constants.ProviderOpenAI: {
			Provider:    constants.ProviderOpenAI,
			ParseModels: (*Server).parseOpenAIModels,
			RenderTyped: renderTypedToOpenAIRequest,
			AnthropicResponse: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaOpenAI
			},
			AnthropicStream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromOpenAI
			},
		},
		constants.ProviderAnthropic: {
			Provider:    constants.ProviderAnthropic,
			ParseModels: (*Server).parseAnthropicModels,
			RenderTyped: renderTypedToAnthropicRequest,
			AnthropicResponse: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaAnthropic
			},
			AnthropicStream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromAnthropic
			},
			OpenAIStream: func(s *Server) openAIStreamForwarder {
				return s.streamOpenAIFromAnthropic
			},
		},
		constants.ProviderGoogle: {
			Provider:    constants.ProviderGoogle,
			ParseModels: (*Server).parseGoogleModels,
			RenderTyped: renderTypedToGoogleRequest,
			AnthropicResponse: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaGoogle
			},
			AnthropicStream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromGoogle
			},
			OpenAIStream: func(s *Server) openAIStreamForwarder {
				return s.streamOpenAIFromGoogle
			},
		},
		constants.ProviderArgo: {
			Provider:    constants.ProviderArgo,
			ParseModels: (*Server).parseArgoModels,
			RenderTyped: renderTypedToArgoRequest,
			AnthropicResponse: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaArgo
			},
			AnthropicStream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromArgo
			},
			OpenAIStream: func(s *Server) openAIStreamForwarder {
				return s.streamOpenAIFromArgo
			},
		},
	}
}

func proxyProviderCapabilityFor(provider string) (proxyProviderCapability, bool) {
	proxyProviderCapabilitiesOnce.Do(initProxyProviderCapabilities)
	capability, ok := proxyProviderCapabilities[constants.NormalizeProvider(provider)]
	return capability, ok
}

func (capability proxyProviderCapability) displayName() string {
	if info, ok := providers.InfoFor(capability.Provider); ok {
		return info.DisplayName
	}
	if capability.Provider != "" {
		return capability.Provider
	}
	return "unknown"
}

func (capability proxyProviderCapability) requireAnthropicResponseForwarder(s *Server) (anthropicResponseForwarder, error) {
	if capability.AnthropicResponse == nil {
		return nil, fmt.Errorf("route request: unknown provider: %s", capability.Provider)
	}
	return capability.AnthropicResponse(s), nil
}

func (capability proxyProviderCapability) requireAnthropicStreamForwarder(s *Server) (anthropicStreamForwarder, error) {
	if capability.AnthropicStream == nil {
		return nil, fmt.Errorf("stream request: unknown provider: %s", capability.Provider)
	}
	return capability.AnthropicStream(s), nil
}

func (capability proxyProviderCapability) requireOpenAIStreamForwarder(s *Server) (openAIStreamForwarder, error) {
	if capability.OpenAIStream == nil {
		return nil, fmt.Errorf("unsupported provider for streaming: %s", capability.Provider)
	}
	return capability.OpenAIStream(s), nil
}
