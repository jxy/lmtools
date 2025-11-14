package core

import (
	"testing"
)

func TestMarshalOpenAIMessagesForRequest_UsesToMap(t *testing.T) {
	msgs := []OpenAIMessage{
		{
			Role: "user",
			Content: OpenAIContentUnion{Contents: []OpenAIContent{
				{Type: "text", Text: "Hello"},
				{Type: "image_url", ImageURL: &OpenAIImageURL{URL: "https://x/y.png", Detail: "high"}},
			}},
		},
	}
	got := MarshalOpenAIMessagesForRequest(msgs)
	m := got[0].(map[string]interface{})
	arr, ok := m["content"].([]interface{})
	if !ok || len(arr) != 2 {
		t.Fatalf("expected 2 content items array, got: %#v", m["content"])
	}
	// First item: text
	c0 := arr[0].(map[string]interface{})
	if c0["type"] != "text" || c0["text"] != "Hello" {
		t.Errorf("first content mismatch: %#v", c0)
	}
	// Second item: image_url map
	c1 := arr[1].(map[string]interface{})
	if c1["type"] != "image_url" {
		t.Errorf("second content type mismatch: %#v", c1)
	}
	img := c1["image_url"].(map[string]interface{})
	if img["url"] != "https://x/y.png" || img["detail"] != "high" {
		t.Errorf("image_url fields mismatch: %#v", img)
	}
}

func TestMarshalAnthropicMessagesForRequest_UsesToMap(t *testing.T) {
	msgs := []AnthropicMessage{
		{
			Role: "assistant",
			Content: AnthropicContentUnion{Contents: []AnthropicContent{
				{Type: "text", Text: "Hi"},
				{Type: "tool_use", ID: "t1", Name: "sum", Input: []byte(`{"a":1}`)},
			}},
		},
	}
	got := MarshalAnthropicMessagesForRequest(msgs)
	m := got[0].(map[string]interface{})
	arr, ok := m["content"].([]interface{})
	if !ok || len(arr) != 2 {
		t.Fatalf("expected 2 content items array, got: %#v", m["content"])
	}
	c0 := arr[0].(map[string]interface{})
	if c0["type"] != "text" || c0["text"] != "Hi" {
		t.Errorf("first content mismatch: %#v", c0)
	}
	c1 := arr[1].(map[string]interface{})
	if c1["type"] != "tool_use" || c1["id"] != "t1" || c1["name"] != "sum" {
		t.Errorf("tool_use mapping mismatch: %#v", c1)
	}
	// Input should be present (json.RawMessage or []byte)
	if c1["input"] == nil {
		t.Errorf("input field missing: %#v", c1)
	}
}
