package proxy

import (
	"context"
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

type modelProviderCapability struct {
	Provider    string
	ParseModels func(*Server, context.Context, []byte) ([]ModelItem, error)
}

type anthropicForwardingPolicy struct {
	Provider string
	Response anthropicResponseForwarderFactory
	Stream   anthropicStreamForwarderFactory
}

type openAIStreamPolicy struct {
	Provider string
	Stream   openAIStreamForwarderFactory
}

var (
	modelProviderCapabilities     map[string]modelProviderCapability
	modelProviderCapabilitiesOnce sync.Once
	anthropicForwardingPolicies   map[string]anthropicForwardingPolicy
	openAIStreamPolicies          map[string]openAIStreamPolicy
	forwardingPoliciesOnce        sync.Once
)

func initModelProviderCapabilities() {
	modelProviderCapabilities = map[string]modelProviderCapability{
		constants.ProviderOpenAI: {
			Provider:    constants.ProviderOpenAI,
			ParseModels: (*Server).parseOpenAIModelsForProvider,
		},
		constants.ProviderAnthropic: {
			Provider:    constants.ProviderAnthropic,
			ParseModels: (*Server).parseAnthropicModelsForProvider,
		},
		constants.ProviderGoogle: {
			Provider:    constants.ProviderGoogle,
			ParseModels: (*Server).parseGoogleModelsForProvider,
		},
		constants.ProviderArgo: {
			Provider:    constants.ProviderArgo,
			ParseModels: (*Server).parseArgoModelsForProvider,
		},
	}
}

func initForwardingPolicies() {
	anthropicForwardingPolicies = map[string]anthropicForwardingPolicy{
		constants.ProviderOpenAI: {
			Provider: constants.ProviderOpenAI,
			Response: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaOpenAI
			},
			Stream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromOpenAI
			},
		},
		constants.ProviderAnthropic: {
			Provider: constants.ProviderAnthropic,
			Response: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaAnthropic
			},
			Stream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromAnthropic
			},
		},
		constants.ProviderGoogle: {
			Provider: constants.ProviderGoogle,
			Response: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaGoogle
			},
			Stream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromGoogle
			},
		},
		constants.ProviderArgo: {
			Provider: constants.ProviderArgo,
			Response: func(s *Server) anthropicResponseForwarder {
				return s.forwardAnthropicViaArgo
			},
			Stream: func(s *Server) anthropicStreamForwarder {
				return s.streamFromArgo
			},
		},
	}

	openAIStreamPolicies = map[string]openAIStreamPolicy{
		constants.ProviderAnthropic: {
			Provider: constants.ProviderAnthropic,
			Stream: func(s *Server) openAIStreamForwarder {
				return s.streamOpenAIFromAnthropic
			},
		},
		constants.ProviderGoogle: {
			Provider: constants.ProviderGoogle,
			Stream: func(s *Server) openAIStreamForwarder {
				return s.streamOpenAIFromGoogle
			},
		},
		constants.ProviderArgo: {
			Provider: constants.ProviderArgo,
			Stream: func(s *Server) openAIStreamForwarder {
				return s.streamOpenAIFromArgo
			},
		},
	}
}

func modelProviderCapabilityFor(provider string) (modelProviderCapability, bool) {
	modelProviderCapabilitiesOnce.Do(initModelProviderCapabilities)
	capability, ok := modelProviderCapabilities[constants.NormalizeProvider(provider)]
	return capability, ok
}

func anthropicForwardingPolicyFor(provider string) (anthropicForwardingPolicy, bool) {
	forwardingPoliciesOnce.Do(initForwardingPolicies)
	policy, ok := anthropicForwardingPolicies[constants.NormalizeProvider(provider)]
	return policy, ok
}

func openAIStreamPolicyFor(provider string) (openAIStreamPolicy, bool) {
	forwardingPoliciesOnce.Do(initForwardingPolicies)
	policy, ok := openAIStreamPolicies[constants.NormalizeProvider(provider)]
	return policy, ok
}

func (capability modelProviderCapability) displayName() string {
	if info, ok := providers.InfoFor(capability.Provider); ok {
		return info.DisplayName
	}
	if capability.Provider != "" {
		return capability.Provider
	}
	return "unknown"
}

func (policy anthropicForwardingPolicy) requireResponseForwarder(s *Server) (anthropicResponseForwarder, error) {
	if policy.Response == nil {
		return nil, fmt.Errorf("route request: unknown provider: %s", policy.Provider)
	}
	return policy.Response(s), nil
}

func (policy anthropicForwardingPolicy) requireStreamForwarder(s *Server) (anthropicStreamForwarder, error) {
	if policy.Stream == nil {
		return nil, fmt.Errorf("stream request: unknown provider: %s", policy.Provider)
	}
	return policy.Stream(s), nil
}

func (policy openAIStreamPolicy) requireStreamForwarder(s *Server) (openAIStreamForwarder, error) {
	if policy.Stream == nil {
		return nil, fmt.Errorf("unsupported provider for streaming: %s", policy.Provider)
	}
	return policy.Stream(s), nil
}
