package proxy

// ARCHITECTURAL PRINCIPLE: The Converter is responsible for transforming between
// provider-specific formats (OpenAI, Anthropic, Google, Argo) at API boundaries.
// All conversions MUST go through TypedRequest as the canonical intermediate
// representation. This ensures:
// 1. Consistent message handling across all providers
// 2. Single source of truth for business logic
// 3. Provider-specific details remain isolated
//
// NEVER convert directly between provider formats. The flow is always:
//   Provider A Format -> TypedRequest -> Provider B Format
//
// This principle ensures maintainability and reduces bugs from format inconsistencies.

// Converter handles conversions between different API formats
type Converter struct {
	mapper *ModelMapper
}

// NewConverter creates a new converter
func NewConverter(mapper *ModelMapper) *Converter {
	return &Converter{mapper: mapper}
}
