package modelcatalog

import (
	"lmtools/internal/apifixtures"
	"testing"
)

func parseCapture(t *testing.T, provider, caseID, file string) map[string]Item {
	t.Helper()
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	data, err := apifixtures.ReadCaseFile(suite.Root, caseID, "captures/"+file)
	if err != nil {
		t.Fatal(err)
	}
	items, err := Parse(provider, data)
	if err != nil {
		t.Fatalf("%s: %v", provider, err)
	}
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	return byID
}

func TestDescribeReadsWhatEachProviderReports(t *testing.T) {
	const minerva = "context 1048576 tokens; input text, image; thinking; effort none, minimal, low, medium, high, xhigh, max (default high); tools; APIs chat_completions, responses, messages"
	for _, tt := range []struct {
		name, provider, caseID, file, model, want string
	}{
		{"ALCF gateway as openai", "openai", "response-openai-vllm-reasoning", "minerva-models.json", "inkling-bf16", minerva},
		{"ALCF gateway as anthropic", "anthropic", "response-openai-vllm-reasoning", "minerva-models.json", "inkling-bf16", minerva},
		{
			"ALCF gateway, text only", "openai", "response-openai-vllm-reasoning", "minerva-models.json", "beam-1.0-bf16",
			"context 1048576 tokens; input text; thinking; effort minimal, low, medium, high, xhigh, max (default max); tools; APIs chat_completions, responses, messages",
		},
		{
			"Anthropic", "anthropic", "models-anthropic", "anthropic.response.json", "claude-opus-4-7",
			"context 1000000 tokens; output 128000 tokens; input text, image, pdf; thinking (adaptive); effort low, medium, high, max; structured output",
		},
		{"Google", "google", "models-google", "google.response.json", "gemini-2.5-flash", "context 1048576 tokens; output 65536 tokens; thinking"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item, ok := parseCapture(t, tt.provider, tt.caseID, tt.file)[tt.model]
			if !ok {
				t.Fatalf("no %s in the capture", tt.model)
			}
			if got := Describe(item).String(); got != tt.want {
				t.Fatalf("Describe(%s) =\n  %s\nwant\n  %s", tt.model, got, tt.want)
			}
		})
	}
}

func TestDescribeLeavesUndescribedModelsEmpty(t *testing.T) {
	for _, provider := range []string{"openai", "argo"} {
		for id, item := range parseCapture(t, provider, "models-"+provider, provider+".response.json") {
			if got := Describe(item).String(); got != "" {
				t.Fatalf("%s %s: Describe = %q, want nothing", provider, id, got)
			}
		}
	}
}
