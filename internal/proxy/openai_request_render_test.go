package proxy

import (
	"lmtools/internal/core"
	"testing"
)

func TestTypedToOpenAIRequest_PreservesVerbosity(t *testing.T) {
	req, err := TypedToOpenAIRequest(TypedRequest{Verbosity: "high"}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}
	if req.Verbosity != "high" {
		t.Fatalf("Verbosity = %q, want high", req.Verbosity)
	}
}

// Every model's output limit goes out as max_completion_tokens, whatever the
// model is called: Argo's names leave the hyphen out of gpt-5, and a later
// family such as gpt-6 is in no list. The developer role still follows the
// model's name.
func TestTypedToOpenAIRequestLimitsOutputWithMaxCompletionTokens(t *testing.T) {
	maxTokens := 128
	for _, tt := range []struct {
		model string
		role  core.Role // the system instructions' role, when the name decides it
	}{
		{"gpt-5.4-nano", core.RoleDeveloper},
		{"o3-mini", core.RoleDeveloper},
		{"gpt-4o", core.RoleSystem},
		{"gpt55", ""},
		{"gpt-6-astra", ""},
		{"gemini4argon", ""},
	} {
		t.Run(tt.model, func(t *testing.T) {
			req, err := TypedToOpenAIRequest(TypedRequest{MaxTokens: &maxTokens, System: "be brief"}, tt.model)
			if err != nil {
				t.Fatalf("TypedToOpenAIRequest() error = %v", err)
			}
			if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != 128 || req.MaxTokens != nil {
				t.Fatalf("max_completion_tokens = %v, max_tokens = %v; want 128 and none", req.MaxCompletionTokens, req.MaxTokens)
			}
			if tt.role != "" && (len(req.Messages) == 0 || req.Messages[0].Role != tt.role) {
				t.Fatalf("messages = %+v, want the system instructions as %s", req.Messages, tt.role)
			}
		})
	}
}

func TestTypedToOpenAIRequest_OmitsNonPositiveMaxCompletionTokens(t *testing.T) {
	maxTokens := 0
	req, err := TypedToOpenAIRequest(TypedRequest{MaxTokens: &maxTokens}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}
	if req.MaxCompletionTokens != nil {
		t.Fatalf("MaxCompletionTokens = %v, want nil", req.MaxCompletionTokens)
	}
	if req.MaxTokens != nil {
		t.Fatalf("MaxTokens = %v, want nil", req.MaxTokens)
	}
}
