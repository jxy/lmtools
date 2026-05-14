package proxy

import "testing"

func TestTypedToOpenAIRequest_UsesMaxCompletionTokensForGPT5Family(t *testing.T) {
	maxTokens := 128
	req, err := TypedToOpenAIRequest(TypedRequest{MaxTokens: &maxTokens}, "gpt-5.4-nano")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}
	if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != 128 {
		t.Fatalf("MaxCompletionTokens = %v, want 128", req.MaxCompletionTokens)
	}
	if req.MaxTokens != nil {
		t.Fatalf("MaxTokens = %v, want nil", req.MaxTokens)
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
