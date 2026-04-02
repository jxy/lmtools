package providers

import "fmt"

type ValidationSurface string

const (
	ValidationSurfaceCLI   ValidationSurface = "cli"
	ValidationSurfaceProxy ValidationSurface = "proxy"
)

type CredentialState struct {
	ProviderURL bool
	APIKey      bool
	ArgoUser    bool
}

type CredentialPolicy struct {
	AllowProviderURL      bool
	ValidationError       string
	MissingCredentialText string
}

func credentialPolicyFor(provider string, surface ValidationSurface) (CredentialPolicy, bool) {
	descriptor, ok := descriptorFor(provider)
	if !ok {
		return CredentialPolicy{}, false
	}
	policy, ok := descriptor.Policies[surface]
	return policy, ok
}

func ValidationError(provider string, surface ValidationSurface) string {
	policy, ok := credentialPolicyFor(provider, surface)
	if !ok {
		return "invalid provider configuration"
	}
	return policy.ValidationError
}

func EvaluateCredentialState(provider string, state CredentialState, surface ValidationSurface) (bool, string) {
	info, ok := InfoFor(provider)
	if !ok {
		return false, fmt.Sprintf("Provider=%s: unknown provider", provider)
	}

	policy, ok := credentialPolicyFor(provider, surface)
	if !ok {
		return false, fmt.Sprintf("Provider=%s: unknown validation surface", provider)
	}

	switch info.CredentialKind {
	case CredentialKindNone:
		return true, ""
	case CredentialKindAPIKey:
		if state.APIKey || (policy.AllowProviderURL && state.ProviderURL) {
			return true, ""
		}
	case CredentialKindArgoUser:
		if state.ArgoUser || (policy.AllowProviderURL && state.ProviderURL) {
			return true, ""
		}
	default:
		return false, fmt.Sprintf("Provider=%s: unsupported credential kind %q", provider, info.CredentialKind)
	}

	return false, policy.MissingCredentialText
}
