package core

import (
	"encoding/json"
	"testing"
)

// imageTestJSON normalizes a renderer's output through JSON so assertions do
// not depend on whether a layer used []interface{} or []map[string]interface{}.
func imageTestJSON(t *testing.T, value interface{}) interface{} {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

const googleTestDataURL = "data:image/png;base64,iVBORw0KGgo="

// googleImageParts renders one user message and returns its two Gemini parts.
func googleImageParts(t *testing.T, blocks ...Block) []interface{} {
	t.Helper()
	rendered := imageTestJSON(t, MarshalGoogleMessagesForRequest(ToGoogleTyped([]TypedMessage{{
		Role:   string(RoleUser),
		Blocks: blocks,
	}})))
	googleMessages, ok := rendered.([]interface{})
	if !ok || len(googleMessages) != 1 {
		t.Fatalf("rendered = %#v, want one message", rendered)
	}
	parts, ok := googleMessages[0].(map[string]interface{})["parts"].([]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("parts = %#v, want the image part then the text part", parts)
	}
	return parts
}

func TestGoogleRendersDataURLImageAsInlineData(t *testing.T) {
	parts := googleImageParts(t, ImageBlock{URL: googleTestDataURL, Detail: "high"}, TextBlock{Text: "what is this?"})

	imagePart := parts[0].(map[string]interface{})
	inline, ok := imagePart["inlineData"].(map[string]interface{})
	if !ok {
		t.Fatalf("parts[0] = %#v, want an inlineData part", imagePart)
	}
	if inline["mimeType"] != "image/png" || inline["data"] != "iVBORw0KGgo=" {
		t.Fatalf("inlineData = %#v, want image/png with the payload", inline)
	}
	if _, has := imagePart["text"]; has {
		t.Fatalf("inlineData part also carries text: %#v", imagePart)
	}
	if text := parts[1].(map[string]interface{})["text"]; text != "what is this?" {
		t.Fatalf("parts[1].text = %v, want the prompt", text)
	}
}

func TestGoogleKeepsTextMentionForURLImage(t *testing.T) {
	parts := googleImageParts(t, ImageBlock{URL: "https://example.com/a.png"}, TextBlock{Text: "look"})

	first := parts[0].(map[string]interface{})
	if _, has := first["inlineData"]; has {
		t.Fatalf("URL image rendered as inlineData: %#v", first)
	}
	if first["text"] != "[Image: https://example.com/a.png]" {
		t.Fatalf("parts[0] = %#v, want the text mention", first)
	}
}
