package providers

import "lmtools/internal/constants"

// providerDescriptor is the single source of truth for provider-specific
// metadata, credential validation policy, and endpoint resolution.
type providerDescriptor struct {
	Info             Info
	Policies         map[ValidationSurface]CredentialPolicy
	ResolveEndpoints endpointResolver
}

// providerDescriptors enumerates every supported provider and its complete
// registry entry.
var providerDescriptors = map[string]providerDescriptor{
	constants.ProviderArgo: {
		Info: Info{
			ID:                     constants.ProviderArgo,
			DisplayName:            "Argo",
			DefaultChatModel:       DefaultArgoChatModel,
			DefaultSmallModel:      DefaultArgoSmallModel,
			SupportsEmbeddings:     true,
			SupportsModelsEndpoint: true,
			CredentialKind:         CredentialKindArgoUser,
		},
		Policies: map[ValidationSurface]CredentialPolicy{
			ValidationSurfaceCLI: {
				AllowProviderURL:      false,
				ValidationError:       "user identifier (-argo-user) is required for Argo provider",
				MissingCredentialText: "Provider=argo: missing ArgoUser",
			},
			ValidationSurfaceProxy: {
				AllowProviderURL:      true,
				ValidationError:       "-argo-user is required when -provider is 'argo' (unless using -provider-url)",
				MissingCredentialText: "Provider=argo: missing ArgoUser or ProviderURL",
			},
		},
		ResolveEndpoints: resolveArgoEndpoints,
	},
	constants.ProviderOpenAI: {
		Info: Info{
			ID:                     constants.ProviderOpenAI,
			DisplayName:            "OpenAI",
			DefaultChatModel:       DefaultOpenAIChatModel,
			DefaultSmallModel:      DefaultOpenAISmallModel,
			SupportsEmbeddings:     true,
			SupportsModelsEndpoint: true,
			CredentialKind:         CredentialKindAPIKey,
		},
		Policies: map[ValidationSurface]CredentialPolicy{
			ValidationSurfaceCLI: {
				AllowProviderURL:      true,
				ValidationError:       "-api-key-file is required for openai provider when not using custom -provider-url",
				MissingCredentialText: "Provider=openai: missing APIKey or ProviderURL",
			},
			ValidationSurfaceProxy: {
				AllowProviderURL:      true,
				ValidationError:       "-api-key-file is required when -provider is 'openai' (unless using -provider-url)",
				MissingCredentialText: "Provider=openai: missing credentials (API key or ProviderURL)",
			},
		},
		ResolveEndpoints: resolveOpenAIEndpoints,
	},
	constants.ProviderAnthropic: {
		Info: Info{
			ID:                        constants.ProviderAnthropic,
			DisplayName:               "Anthropic",
			DefaultChatModel:          DefaultAnthropicChatModel,
			DefaultSmallModel:         DefaultAnthropicSmallModel,
			UsesOutOfBandSystemPrompt: true,
			SupportsModelsEndpoint:    true,
			CredentialKind:            CredentialKindAPIKey,
		},
		Policies: map[ValidationSurface]CredentialPolicy{
			ValidationSurfaceCLI: {
				AllowProviderURL:      true,
				ValidationError:       "-api-key-file is required for anthropic provider when not using custom -provider-url",
				MissingCredentialText: "Provider=anthropic: missing APIKey or ProviderURL",
			},
			ValidationSurfaceProxy: {
				AllowProviderURL:      true,
				ValidationError:       "-api-key-file is required when -provider is 'anthropic' (unless using -provider-url)",
				MissingCredentialText: "Provider=anthropic: missing credentials (API key or ProviderURL)",
			},
		},
		ResolveEndpoints: resolveAnthropicEndpoints,
	},
	constants.ProviderGoogle: {
		Info: Info{
			ID:                        constants.ProviderGoogle,
			DisplayName:               "Google",
			DefaultChatModel:          DefaultGoogleChatModel,
			DefaultSmallModel:         DefaultGoogleSmallModel,
			UsesOutOfBandSystemPrompt: true,
			SupportsModelsEndpoint:    true,
			CredentialKind:            CredentialKindAPIKey,
		},
		Policies: map[ValidationSurface]CredentialPolicy{
			ValidationSurfaceCLI: {
				AllowProviderURL:      true,
				ValidationError:       "-api-key-file is required for google provider when not using custom -provider-url",
				MissingCredentialText: "Provider=google: missing APIKey or ProviderURL",
			},
			ValidationSurfaceProxy: {
				AllowProviderURL:      true,
				ValidationError:       "-api-key-file is required when -provider is 'google' (unless using -provider-url)",
				MissingCredentialText: "Provider=google: missing credentials (API key or ProviderURL)",
			},
		},
		ResolveEndpoints: resolveGoogleEndpoints,
	},
}

func descriptorFor(provider string) (providerDescriptor, bool) {
	descriptor, ok := providerDescriptors[constants.NormalizeProvider(provider)]
	return descriptor, ok
}
