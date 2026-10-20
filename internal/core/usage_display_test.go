package core

import "testing"

func TestUsageDisplayPreservesReportedScopes(t *testing.T) {
	for _, tt := range []struct{ name, payload, want string }{
		{"nested details", `{"usage":{"prompt_tokens":381,"completion_tokens":320,"prompt_tokens_details":{"cached_tokens":256},"completion_tokens_details":{"reasoning_tokens":251},"total_tokens":701}}`, "input 381 (cached 256) | output 320 (reasoning 251) | total 701"},
		{"separate cache", `{"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}`, "input 10 | output 20 | cache read 30 | cache write 40"},
		{"separate reasoning", `{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"thoughtsTokenCount":30,"totalTokenCount":60}}`, "input 10 | output 20 | reasoning 30 | total 60"},
		{"detail without parent", `{"usage":{"output_tokens_details":{"reasoning_tokens":8}}}`, "reasoning 8"},
		{"explicit cache wins", `{"usage":{"input_tokens":10,"cache_read_input_tokens":30,"input_tokens_details":{"cached_tokens":5}}}`, "input 10 | cache read 30"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usage := UsageFromPayload([]byte(tt.payload))
			if got := usage.DisplaySummary(); got != tt.want {
				t.Fatalf("summary=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestUsageMergeRetainsDetailScope(t *testing.T) {
	first := UsageFromPayload([]byte(`{"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":5}}}`))
	last := UsageFromPayload([]byte(`{"usage":{"output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}}`))
	if got := first.MergedWith(last).DisplaySummary(); got != "input 10 (cached 5) | output 20 (reasoning 8)" {
		t.Fatal(got)
	}
}
