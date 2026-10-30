package main

import (
	"bytes"
	"lmtools/internal/apifixtures"
	"lmtools/internal/modelcatalog"
	"testing"
)

// -list-models prints a model's features under it when the provider
// describes them, as the ALCF gateway does, and the bare list otherwise.
func TestDisplayModelsShowsDescribedFeatures(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	data, err := apifixtures.ReadCaseFile(suite.Root, "response-openai-vllm-reasoning", "captures/minerva-models.json")
	if err != nil {
		t.Fatal(err)
	}
	items, err := modelcatalog.Parse("openai", data)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	displayModels(&out, "openai", items[:2])
	want := "Available OpenAI models:\n" +
		"  beam-1.0-bf16 (Beam 1.0 BF16)\n" +
		"    context 1048576 tokens; input text; thinking; effort minimal, low, medium, high, xhigh, max (default max); tools; APIs chat_completions, responses, messages\n" +
		"  inkling-bf16 (Inkling BF16)\n" +
		"    context 1048576 tokens; input text, image; thinking; effort none, minimal, low, medium, high, xhigh, max (default high); tools; APIs chat_completions, responses, messages\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}

	out.Reset()
	displayModels(&out, "openai", []modelcatalog.Item{{ID: "gpt-5"}})
	if out.String() != "Available OpenAI models:\n  gpt-5\n" {
		t.Fatalf("undescribed model:\n%s", out.String())
	}
}
