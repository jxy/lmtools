package apifixtures

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type CompareResult struct {
	Kind        string
	Differences []string
}

type openAIStreamSignature struct {
	InitialHasRole      bool
	InitialContentKind  string
	InitialHasToolCalls bool
	FinishReason        string
	HasDone             bool
	HasToolArgChunk     bool
}

type anthropicStreamSignature struct {
	FirstBlockType        string
	FirstToolInputIsEmpty bool
	StopReason            string
	HasMessageStop        bool
}

type shapeCompareOptions struct {
	IgnoreExactPaths             map[string]struct{}
	IgnorePathPrefixes           []string
	NormalizeKinds               func(path, kinds string) string
	TreatNullMissingAsEquivalent bool
}

// CompareCaptureShape compares raw capture payloads without mutating the fixture corpus.
// Non-stream targets are compared by JSON shape. Stream targets use provider-aware SSE
// projections for OpenAI and Anthropic, a generic SSE projection for other SSE targets,
// and fall back to raw stream equality if the payload is not SSE.
func CompareCaptureShape(target CaptureTarget, expected, actual []byte) (CompareResult, error) {
	if !target.Stream {
		return compareJSONShapeForProvider(target.Provider, expected, actual)
	}
	return compareStreamShape(target.Provider, expected, actual)
}

// CompareJSONShape compares two JSON payloads by structural shape rather than values.
func CompareJSONShape(expected, actual []byte) (CompareResult, error) {
	expectedShape, err := JSONShape(expected)
	if err != nil {
		return CompareResult{}, err
	}
	actualShape, err := JSONShape(actual)
	if err != nil {
		return CompareResult{}, err
	}
	return compareShapeMaps("json-shape", expectedShape, actualShape), nil
}

func compareJSONShapeForProvider(provider string, expected, actual []byte) (CompareResult, error) {
	expectedShape, err := JSONShape(expected)
	if err != nil {
		return CompareResult{}, err
	}
	actualShape, err := JSONShape(actual)
	if err != nil {
		return CompareResult{}, err
	}
	opts := jsonShapeOptions(provider)
	if provider == "openai" && (hasShapePath(expectedShape, "$.choices[].message.tool_calls") || hasShapePath(actualShape, "$.choices[].message.tool_calls")) {
		if opts.IgnoreExactPaths == nil {
			opts.IgnoreExactPaths = map[string]struct{}{}
		}
		opts.IgnoreExactPaths["$.choices[].message.content"] = struct{}{}
	}
	return compareShapeMapsWithOptions("json-shape", expectedShape, actualShape, opts), nil
}

// JSONShape returns a normalized path->type-set mapping for the JSON payload.
func JSONShape(data []byte) (map[string]string, error) {
	var decoded interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return shapeOfValue(decoded), nil
}

func compareStreamShape(provider string, expected, actual []byte) (CompareResult, error) {
	switch provider {
	case "openai":
		expectedProjected := projectOpenAIStream(string(expected))
		actualProjected := projectOpenAIStream(string(actual))
		result := compareShapeMapsWithOptions("sse-shape", shapeOfValue(expectedProjected), shapeOfValue(actualProjected), openAIStreamShapeOptions())
		result.Differences = append(result.Differences, diffOpenAIStreamSignature(
			projectOpenAISignature(expectedProjected),
			projectOpenAISignature(actualProjected),
		)...)
		sort.Strings(result.Differences)
		return result, nil
	case "anthropic":
		expectedProjected := projectAnthropicStream(string(expected))
		actualProjected := projectAnthropicStream(string(actual))
		result := compareShapeMapsWithOptions("sse-shape", shapeOfValue(expectedProjected), shapeOfValue(actualProjected), anthropicStreamShapeOptions())
		result.Differences = append(result.Differences, diffAnthropicStreamSignature(
			projectAnthropicSignature(expectedProjected),
			projectAnthropicSignature(actualProjected),
		)...)
		sort.Strings(result.Differences)
		return result, nil
	default:
		expectedProjected, expectedSSE := projectGenericSSEStream(string(expected))
		actualProjected, actualSSE := projectGenericSSEStream(string(actual))
		switch {
		case expectedSSE && actualSSE:
			result := compareShapeMaps("sse-shape", shapeOfValue(expectedProjected), shapeOfValue(actualProjected))
			result.Differences = append(result.Differences, diffGenericSSEFeatures(expectedProjected, actualProjected)...)
			sort.Strings(result.Differences)
			return result, nil
		case expectedSSE != actualSSE:
			return CompareResult{
				Kind: "sse-shape",
				Differences: []string{
					fmt.Sprintf("stream framing changed: expected_sse=%t actual_sse=%t", expectedSSE, actualSSE),
				},
			}, nil
		default:
			if string(expected) == string(actual) {
				return CompareResult{Kind: "raw-stream"}, nil
			}
			return CompareResult{
				Kind:        "raw-stream",
				Differences: []string{"raw stream body differs"},
			}, nil
		}
	}
}

func compareShapeMaps(kind string, expected, actual map[string]string) CompareResult {
	return compareShapeMapsWithOptions(kind, expected, actual, shapeCompareOptions{})
}

func compareShapeMapsWithOptions(kind string, expected, actual map[string]string, opts shapeCompareOptions) CompareResult {
	differences := make([]string, 0)
	seen := make(map[string]struct{}, len(expected)+len(actual))
	paths := make([]string, 0, len(expected)+len(actual))
	for path := range expected {
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	for path := range actual {
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	for _, path := range paths {
		if shouldIgnorePath(path, opts) {
			continue
		}
		want, wantOK := expected[path]
		got, gotOK := actual[path]
		if opts.NormalizeKinds != nil {
			if wantOK {
				want = opts.NormalizeKinds(path, want)
			}
			if gotOK {
				got = opts.NormalizeKinds(path, got)
			}
		}
		switch {
		case wantOK && !gotOK:
			if opts.TreatNullMissingAsEquivalent && isNullOnlyShape(want) {
				continue
			}
			differences = append(differences, fmt.Sprintf("missing path %s (expected types: %s)", path, want))
		case !wantOK && gotOK:
			if opts.TreatNullMissingAsEquivalent && isNullOnlyShape(got) {
				continue
			}
			differences = append(differences, fmt.Sprintf("unexpected path %s (actual types: %s)", path, got))
		case want != got:
			differences = append(differences, fmt.Sprintf("type mismatch at %s (expected %s, got %s)", path, want, got))
		}
	}

	return CompareResult{
		Kind:        kind,
		Differences: differences,
	}
}

func shouldIgnorePath(path string, opts shapeCompareOptions) bool {
	if len(opts.IgnoreExactPaths) > 0 {
		if _, ok := opts.IgnoreExactPaths[path]; ok {
			return true
		}
	}
	for _, prefix := range opts.IgnorePathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isNullOnlyShape(kinds string) bool {
	return kinds == "null"
}

func hasShapePath(shape map[string]string, path string) bool {
	_, ok := shape[path]
	return ok
}

func jsonShapeOptions(provider string) shapeCompareOptions {
	opts := shapeCompareOptions{
		TreatNullMissingAsEquivalent: true,
	}
	switch provider {
	case "openai":
		opts.IgnorePathPrefixes = []string{
			"$.choices[].content_filter_results",
			"$.choices[].message.annotations",
			"$.prompt_filter_results",
			"$.usage.completion_tokens_details",
			"$.usage.prompt_tokens_details",
		}
		opts.IgnoreExactPaths = map[string]struct{}{
			"$.service_tier":       {},
			"$.system_fingerprint": {},
		}
	case "anthropic":
		opts.IgnorePathPrefixes = []string{
			"$.content[].caller",
			"$.usage.server_tool_use",
		}
		opts.IgnoreExactPaths = map[string]struct{}{
			"$.usage.inference_geo": {},
			"$.usage.service_tier":  {},
		}
	default:
		opts.IgnoreExactPaths = map[string]struct{}{}
	}
	return opts
}

func openAIStreamShapeOptions() shapeCompareOptions {
	return shapeCompareOptions{
		TreatNullMissingAsEquivalent: true,
		IgnorePathPrefixes: []string{
			"$[].delta.refusal",
			"$[].usage",
		},
		NormalizeKinds: normalizeOpenAIStreamKinds,
	}
}

func anthropicStreamShapeOptions() shapeCompareOptions {
	return shapeCompareOptions{
		TreatNullMissingAsEquivalent: true,
		IgnorePathPrefixes: []string{
			"$[].usage.server_tool_use",
		},
	}
}

func shapeOfValue(value interface{}) map[string]string {
	var normalized interface{}
	raw, err := json.Marshal(value)
	if err == nil {
		if decodeErr := json.Unmarshal(raw, &normalized); decodeErr == nil {
			value = normalized
		}
	}

	acc := make(map[string]map[string]struct{})
	collectShape(acc, "$", value)
	result := make(map[string]string, len(acc))
	for path, kinds := range acc {
		names := make([]string, 0, len(kinds))
		for kind := range kinds {
			names = append(names, kind)
		}
		sort.Strings(names)
		result[path] = strings.Join(names, "|")
	}
	return result
}

func collectShape(acc map[string]map[string]struct{}, path string, value interface{}) {
	switch typed := value.(type) {
	case nil:
		addShapeKind(acc, path, "null")
	case bool:
		addShapeKind(acc, path, "bool")
	case float64:
		addShapeKind(acc, path, "number")
	case string:
		addShapeKind(acc, path, "string")
	case []interface{}:
		addShapeKind(acc, path, "array")
		if len(typed) == 0 {
			addShapeKind(acc, path+"[]", "empty")
			return
		}
		for _, item := range typed {
			collectShape(acc, path+"[]", item)
		}
	case map[string]interface{}:
		addShapeKind(acc, path, "object")
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectShape(acc, path+"."+key, typed[key])
		}
	default:
		addShapeKind(acc, path, fmt.Sprintf("%T", value))
	}
}

func addShapeKind(acc map[string]map[string]struct{}, path, kind string) {
	if _, ok := acc[path]; !ok {
		acc[path] = make(map[string]struct{})
	}
	acc[path][kind] = struct{}{}
}

func projectAnthropicStream(raw string) []map[string]interface{} {
	lines := strings.Split(raw, "\n")
	var currentEvent string
	var projected []map[string]interface{}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "" {
				continue
			}
			var decoded map[string]interface{}
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				continue
			}

			entry := map[string]interface{}{
				"event": currentEvent,
			}
			if index, ok := decoded["index"]; ok {
				entry["index"] = index
			}
			if delta, ok := decoded["delta"].(map[string]interface{}); ok {
				if deltaType, ok := delta["type"]; ok {
					entry["delta_type"] = deltaType
				}
				if text, ok := delta["text"]; ok {
					entry["text"] = text
				}
				if partialJSON, ok := delta["partial_json"]; ok {
					entry["partial_json"] = partialJSON
				}
				if stopReason, ok := delta["stop_reason"]; ok {
					entry["stop_reason"] = stopReason
				}
			}
			if block, ok := decoded["content_block"].(map[string]interface{}); ok {
				if blockType, ok := block["type"]; ok {
					entry["block_type"] = blockType
				}
				if name, ok := block["name"]; ok {
					entry["name"] = name
				}
			}
			if message, ok := decoded["message"].(map[string]interface{}); ok {
				entry["role"] = message["role"]
				entry["model"] = message["model"]
			}
			if usage, ok := decoded["usage"]; ok {
				entry["usage"] = usage
			}
			projected = append(projected, entry)
		}
	}

	return projected
}

func projectOpenAIStream(raw string) []map[string]interface{} {
	lines := strings.Split(raw, "\n")
	projected := make([]map[string]interface{}, 0)

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			projected = append(projected, map[string]interface{}{"done": true})
			continue
		}

		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			continue
		}

		entry := map[string]interface{}{}
		if usage, ok := decoded["usage"]; ok && usage != nil {
			entry["usage"] = usage
		}
		if choices, ok := decoded["choices"].([]interface{}); ok && len(choices) > 0 {
			choice, _ := choices[0].(map[string]interface{})
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				entry["delta"] = delta
			}
			if finishReason, ok := choice["finish_reason"]; ok && finishReason != nil {
				entry["finish_reason"] = finishReason
			}
		}
		if len(entry) == 0 {
			continue
		}
		projected = append(projected, entry)
	}

	return projected
}

func projectGenericSSEStream(raw string) ([]map[string]interface{}, bool) {
	lines := strings.Split(raw, "\n")
	projected := make([]map[string]interface{}, 0)
	var currentEvent string
	dataLines := make([]string, 0)
	sawSSE := false

	flush := func() {
		if !sawSSE && currentEvent == "" && len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		entry := map[string]interface{}{}
		if currentEvent != "" {
			entry["event"] = currentEvent
		}
		if payload == "[DONE]" {
			entry["done"] = true
		} else if payload != "" {
			var decoded interface{}
			if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
				entry["data"] = decoded
			} else {
				entry["text"] = payload
			}
		}
		if len(entry) > 0 {
			projected = append(projected, entry)
		}
		currentEvent = ""
		dataLines = dataLines[:0]
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event:"):
			sawSSE = true
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			sawSSE = true
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.HasPrefix(line, ":"):
			sawSSE = true
		case line == "":
			if sawSSE || currentEvent != "" || len(dataLines) > 0 {
				flush()
			}
		}
	}

	if sawSSE || currentEvent != "" || len(dataLines) > 0 {
		flush()
	}
	return projected, sawSSE
}

func projectOpenAISignature(projected []map[string]interface{}) openAIStreamSignature {
	sig := openAIStreamSignature{}
	if len(projected) == 0 {
		return sig
	}

	first := projected[0]
	if delta, ok := first["delta"].(map[string]interface{}); ok {
		_, sig.InitialHasRole = delta["role"]
		if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
			sig.InitialHasToolCalls = true
		}
		if content, ok := delta["content"]; ok {
			switch v := content.(type) {
			case nil:
				sig.InitialContentKind = "null"
			case string:
				if v == "" {
					sig.InitialContentKind = "empty"
				} else {
					sig.InitialContentKind = "text"
				}
			default:
				sig.InitialContentKind = "other"
			}
		} else {
			sig.InitialContentKind = "absent"
		}
	}

	for _, entry := range projected {
		if done, _ := entry["done"].(bool); done {
			sig.HasDone = true
		}
		if finishReason, _ := entry["finish_reason"].(string); finishReason != "" {
			sig.FinishReason = finishReason
		}
		delta, _ := entry["delta"].(map[string]interface{})
		toolCalls, _ := delta["tool_calls"].([]interface{})
		for _, rawToolCall := range toolCalls {
			toolCall, _ := rawToolCall.(map[string]interface{})
			function, _ := toolCall["function"].(map[string]interface{})
			if args, _ := function["arguments"].(string); strings.TrimSpace(args) != "" {
				sig.HasToolArgChunk = true
			}
		}
	}

	return sig
}

func projectAnthropicSignature(projected []map[string]interface{}) anthropicStreamSignature {
	sig := anthropicStreamSignature{}
	for _, entry := range projected {
		if sig.FirstBlockType == "" {
			if blockType, _ := entry["block_type"].(string); blockType != "" {
				sig.FirstBlockType = blockType
			}
		}
		if entry["event"] == "content_block_delta" && entry["delta_type"] == "input_json_delta" && entry["partial_json"] == "" {
			sig.FirstToolInputIsEmpty = true
		}
		if entry["event"] == "message_delta" {
			if stopReason, _ := entry["stop_reason"].(string); stopReason != "" {
				sig.StopReason = stopReason
			}
		}
		if entry["event"] == "message_stop" {
			sig.HasMessageStop = true
		}
	}
	return sig
}

func diffOpenAIStreamSignature(expected, actual openAIStreamSignature) []string {
	differences := make([]string, 0)
	expectedContentKind := normalizeOpenAIInitialContentKind(expected.InitialContentKind)
	actualContentKind := normalizeOpenAIInitialContentKind(actual.InitialContentKind)
	if expectedContentKind != actualContentKind {
		differences = append(differences, fmt.Sprintf("openai stream initial content kind changed (expected %s, got %s)", expectedContentKind, actualContentKind))
	}
	if expected.InitialHasToolCalls != actual.InitialHasToolCalls {
		differences = append(differences, fmt.Sprintf("openai stream initial tool-call presence changed (expected %t, got %t)", expected.InitialHasToolCalls, actual.InitialHasToolCalls))
	}
	expectedFinishReason := normalizeOpenAIFinishReason(expected.FinishReason)
	actualFinishReason := normalizeOpenAIFinishReason(actual.FinishReason)
	if expectedFinishReason != actualFinishReason {
		differences = append(differences, fmt.Sprintf("openai stream finish_reason changed (expected %s, got %s)", expected.FinishReason, actual.FinishReason))
	}
	if expected.HasDone != actual.HasDone {
		differences = append(differences, fmt.Sprintf("openai stream [DONE] presence changed (expected %t, got %t)", expected.HasDone, actual.HasDone))
	}
	if expected.HasToolArgChunk != actual.HasToolArgChunk {
		differences = append(differences, fmt.Sprintf("openai stream tool argument chunk presence changed (expected %t, got %t)", expected.HasToolArgChunk, actual.HasToolArgChunk))
	}
	return differences
}

func normalizeOpenAIInitialContentKind(kind string) string {
	switch kind {
	case "", "absent", "empty", "null":
		return "no_text"
	default:
		return kind
	}
}

func normalizeOpenAIFinishReason(reason string) string {
	switch reason {
	case "", "tool_calls", "function_call":
		return reason
	default:
		return "terminal"
	}
}

func normalizeOpenAIStreamKinds(path, kinds string) string {
	switch path {
	case "$[].delta.content":
		return stripNullFromUnion(kinds)
	case "$[].delta.role":
		normalized := stripNullFromUnion(kinds)
		if normalized == "null" {
			return "string"
		}
		return normalized
	default:
		if strings.HasPrefix(path, "$[].delta.tool_calls") {
			return stripNullFromUnion(kinds)
		}
		return kinds
	}
}

func stripNullFromUnion(kinds string) string {
	if !strings.Contains(kinds, "|") {
		return kinds
	}
	parts := strings.Split(kinds, "|")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "null" || part == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	if len(filtered) == 0 {
		return kinds
	}
	sort.Strings(filtered)
	return strings.Join(filtered, "|")
}

func diffAnthropicStreamSignature(expected, actual anthropicStreamSignature) []string {
	differences := make([]string, 0)
	if expected.FirstBlockType != actual.FirstBlockType {
		differences = append(differences, fmt.Sprintf("anthropic stream first block type changed (expected %s, got %s)", expected.FirstBlockType, actual.FirstBlockType))
	}
	if expected.FirstToolInputIsEmpty != actual.FirstToolInputIsEmpty {
		differences = append(differences, fmt.Sprintf("anthropic stream initial empty tool input chunk changed (expected %t, got %t)", expected.FirstToolInputIsEmpty, actual.FirstToolInputIsEmpty))
	}
	if expected.StopReason != actual.StopReason {
		differences = append(differences, fmt.Sprintf("anthropic stream stop_reason changed (expected %s, got %s)", expected.StopReason, actual.StopReason))
	}
	if expected.HasMessageStop != actual.HasMessageStop {
		differences = append(differences, fmt.Sprintf("anthropic stream message_stop presence changed (expected %t, got %t)", expected.HasMessageStop, actual.HasMessageStop))
	}
	return differences
}

func diffGenericSSEFeatures(expected, actual []map[string]interface{}) []string {
	differences := make([]string, 0)

	expectedEvents := collectProjectedStringSet(expected, "event")
	actualEvents := collectProjectedStringSet(actual, "event")
	differences = append(differences, diffStringSets("sse event", expectedEvents, actualEvents)...)

	expectedKinds := collectProjectedKinds(expected)
	actualKinds := collectProjectedKinds(actual)
	differences = append(differences, diffStringSets("sse payload kind", expectedKinds, actualKinds)...)

	return differences
}

func collectProjectedStringSet(projected []map[string]interface{}, key string) []string {
	set := make(map[string]struct{})
	for _, entry := range projected {
		value, _ := entry[key].(string)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func collectProjectedKinds(projected []map[string]interface{}) []string {
	set := make(map[string]struct{})
	for _, entry := range projected {
		switch {
		case entry["done"] == true:
			set["done"] = struct{}{}
		case entry["data"] != nil:
			set["json"] = struct{}{}
		case entry["text"] != nil:
			set["text"] = struct{}{}
		default:
			set["empty"] = struct{}{}
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func diffStringSets(label string, expected, actual []string) []string {
	differences := make([]string, 0)
	expectedSet := make(map[string]struct{}, len(expected))
	actualSet := make(map[string]struct{}, len(actual))
	for _, value := range expected {
		expectedSet[value] = struct{}{}
	}
	for _, value := range actual {
		actualSet[value] = struct{}{}
	}
	for _, value := range expected {
		if _, ok := actualSet[value]; !ok {
			differences = append(differences, fmt.Sprintf("missing %s %q", label, value))
		}
	}
	for _, value := range actual {
		if _, ok := expectedSet[value]; !ok {
			differences = append(differences, fmt.Sprintf("unexpected %s %q", label, value))
		}
	}
	return differences
}
