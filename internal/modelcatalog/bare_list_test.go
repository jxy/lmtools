package modelcatalog

import (
	"lmtools/internal/apifixtures"
	"testing"
)

// The ALCF inference gateway lists a cluster's models as a bare JSON array,
// which -models-url points lmc and apiproxy at, under either provider.
func TestParseReadsABareModelList(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	data, err := apifixtures.ReadCaseFile(suite.Root, "response-openai-vllm-reasoning", "captures/minerva-models.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"beam-1.0-bf16", "inkling-bf16", "mistral-large-4-nvfp4", "muse-spark-bf16"}
	for _, provider := range []string{"openai", "anthropic"} {
		items, err := Parse(provider, data)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		Sort(items)
		if len(items) != len(want) {
			t.Fatalf("%s: items = %+v", provider, items)
		}
		for i, item := range items {
			if item.ID != want[i] || item.Object != "model" {
				t.Fatalf("%s: items[%d] = %+v, want %s", provider, i, item, want[i])
			}
		}
	}
}
