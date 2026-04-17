package apifixtures

import "testing"

func TestCompareJSONShapeIgnoresScalarValueChanges(t *testing.T) {
	expected := []byte(`{"id":"a","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`)
	actual := []byte(`{"id":"b","choices":[{"index":9,"message":{"role":"assistant","content":"different"}}]}`)

	result, err := CompareJSONShape(expected, actual)
	if err != nil {
		t.Fatalf("CompareJSONShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareJSONShapeDetectsMissingField(t *testing.T) {
	expected := []byte(`{"id":"a","usage":{"prompt_tokens":1}}`)
	actual := []byte(`{"id":"a"}`)

	result, err := CompareJSONShape(expected, actual)
	if err != nil {
		t.Fatalf("CompareJSONShape() error = %v", err)
	}
	if len(result.Differences) == 0 {
		t.Fatal("expected differences")
	}
}

func TestCompareCaptureShapeOpenAIJSONIgnoresOptionalMetadataAndNullAbsence(t *testing.T) {
	expected := []byte(`{
	  "choices":[{"message":{"annotations":[],"content":"hello","refusal":null,"role":"assistant"}}],
	  "service_tier":"default",
	  "system_fingerprint":null,
	  "usage":{
	    "completion_tokens":1,
	    "completion_tokens_details":{"reasoning_tokens":0},
	    "prompt_tokens":1,
	    "prompt_tokens_details":{"cached_tokens":0}
	  }
	}`)
	actual := []byte(`{
	  "choices":[{"message":{"content":"different","role":"assistant","tool_calls":null}}],
	  "usage":{"completion_tokens":99,"prompt_tokens":88}
	}`)

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-openai", Provider: "openai", Host: "argo"}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeOpenAIJSONIgnoresCompatibilityFilterMetadata(t *testing.T) {
	expected := []byte(`{
	  "choices":[{"message":{"content":"hello","role":"assistant"}}]
	}`)
	actual := []byte(`{
	  "choices":[{"message":{"content":"different","role":"assistant"},"content_filter_results":{"hate":{"filtered":false}}}],
	  "prompt_filter_results":[{"prompt_index":0,"content_filter_results":{"jailbreak":{"detected":false,"filtered":false}}}]
	}`)

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-openai", Provider: "openai", Host: "argo"}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeOpenAIJSONIgnoresToolCallContentNullability(t *testing.T) {
	expected := []byte(`{
	  "choices":[{"message":{"content":null,"role":"assistant","tool_calls":[{"type":"function"}]}}],
	  "usage":{"completion_tokens":1,"prompt_tokens":1}
	}`)
	actual := []byte(`{
	  "choices":[{"message":{"content":"","role":"assistant","tool_calls":[{"type":"function"}]}}],
	  "usage":{"completion_tokens":2,"prompt_tokens":2}
	}`)

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-openai", Provider: "openai", Host: "argo"}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeAnthropicJSONIgnoresOptionalMetadataAndNullAbsence(t *testing.T) {
	expected := []byte(`{
	  "content":[{"type":"text","text":"hello"}],
	  "usage":{"input_tokens":1,"output_tokens":2,"inference_geo":"not_available","service_tier":"standard"}
	}`)
	actual := []byte(`{
	  "content":[{"type":"text","text":"different","citations":null}],
	  "usage":{"input_tokens":9,"output_tokens":8,"service_tier":null,"server_tool_use":null}
	}`)

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-anthropic", Provider: "anthropic", Host: "argo"}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeAnthropicJSONIgnoresOptionalCallerMetadata(t *testing.T) {
	expected := []byte(`{
	  "content":[{"type":"tool_use","name":"get_weather","input":{},"caller":{"type":"direct"}}]
	}`)
	actual := []byte(`{
	  "content":[{"type":"tool_use","name":"get_weather","input":{}}]
	}`)

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-anthropic", Provider: "anthropic", Host: "argo"}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeOpenAIStreamIgnoresChunkContentChanges(t *testing.T) {
	expected := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: [DONE]\n")
	actual := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"goodbye\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: [DONE]\n")

	result, err := CompareCaptureShape(CaptureTarget{ID: "openai-stream", Provider: "openai", Host: "openai", Stream: true}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeOpenAIStreamIgnoresOptionalUsageAndNoTextInitialChunk(t *testing.T) {
	expected := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"refusal\":null}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: [DONE]\n")
	actual := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"goodbye\"}}]}\n" +
		"data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: [DONE]\n")

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-openai-stream", Provider: "openai", Host: "argo", Stream: true}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeOpenAIStreamIgnoresNullableChunkFieldsAndTerminalFinishVariants(t *testing.T) {
	expected := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n" +
		"data: [DONE]\n")
	actual := []byte("data: {\"choices\":[{\"delta\":{\"content\":null}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":null,\"content\":\"goodbye\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n" +
		"data: [DONE]\n")

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-openai-stream", Provider: "openai", Host: "argo", Stream: true}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeOpenAIStreamIgnoresMetadataOnlyLeadingChunkAndNullableToolCallFields(t *testing.T) {
	expected := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"location\\\":\\\"Chicago\\\"}\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n" +
		"data: [DONE]\n")
	actual := []byte("data: {\"choices\":[],\"prompt_filter_results\":[{\"prompt_index\":0,\"content_filter_results\":{}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":null,\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":null,\"type\":null,\"function\":{\"name\":null,\"arguments\":\"{\\\"location\\\":\\\"Chicago\\\"}\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":null},\"finish_reason\":\"tool_calls\"}]}\n" +
		"data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n" +
		"data: [DONE]\n")

	result, err := CompareCaptureShape(CaptureTarget{ID: "argo-openai-stream", Provider: "openai", Host: "argo", Stream: true}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("differences = %v, want none", result.Differences)
	}
}

func TestCompareCaptureShapeAnthropicStreamDetectsStopReasonChange(t *testing.T) {
	expected := []byte("event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n")
	actual := []byte("event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n")

	result, err := CompareCaptureShape(CaptureTarget{ID: "anthropic-stream", Provider: "anthropic", Host: "anthropic", Stream: true}, expected, actual)
	if err != nil {
		t.Fatalf("CompareCaptureShape() error = %v", err)
	}
	if len(result.Differences) == 0 {
		t.Fatal("expected differences")
	}
}
