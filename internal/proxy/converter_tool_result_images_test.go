package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"testing"
)

// A client on the Anthropic wire returns a screenshot inside a tool_result
// as a content array. The text blocks are the result's text, the image blocks
// are its images, and a block this proxy has no home for keeps its JSON in
// the text so it is not dropped.
func TestAnthropicToolResultArrayContentYieldsTextAndImages(t *testing.T) {
	document := `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"15 degrees"}}`
	blocks := []AnthropicContentBlock{{
		Type:      "tool_result",
		ToolUseID: "tool-1",
		Content: json.RawMessage(`[` +
			`{"type":"text","text":"Read image (8 bytes)"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},` +
			`{"type":"image","source":{"type":"url","url":"https://example.test/shot.png"}},` +
			document +
			`]`),
	}}

	converted := AnthropicBlocksToCore(blocks)
	if len(converted) != 1 {
		t.Fatalf("len(converted) = %d, want 1", len(converted))
	}
	result, ok := converted[0].(core.ToolResultBlock)
	if !ok {
		t.Fatalf("converted[0] type = %T, want core.ToolResultBlock", converted[0])
	}
	if want := "Read image (8 bytes)\n" + document; result.Content != want {
		t.Fatalf("Content = %q, want %q", result.Content, want)
	}
	if len(result.Images) != 2 {
		t.Fatalf("Images = %#v, want the base64 and the url image", result.Images)
	}
	if result.Images[0].URL != "data:image/png;base64,iVBORw0KGgo=" || result.Images[0].Detail != "auto" {
		t.Fatalf("Images[0] = %#v", result.Images[0])
	}
	if result.Images[1].URL != "https://example.test/shot.png" {
		t.Fatalf("Images[1] = %#v", result.Images[1])
	}
}

func TestAnthropicToolResultStringContentHasNoImages(t *testing.T) {
	converted := AnthropicBlocksToCore([]AnthropicContentBlock{{
		Type:      "tool_result",
		ToolUseID: "tool-1",
		Content:   json.RawMessage(`"plain text"`),
	}})
	result := converted[0].(core.ToolResultBlock)
	if result.Content != "plain text" || result.Images != nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnthropicToolResultImageWithoutSourceIsDropped(t *testing.T) {
	converted := AnthropicBlocksToCore([]AnthropicContentBlock{{
		Type:      "tool_result",
		ToolUseID: "tool-1",
		Content:   json.RawMessage(`[{"type":"image"},{"type":"text","text":"kept"}]`),
	}})
	result := converted[0].(core.ToolResultBlock)
	if result.Content != "kept" || len(result.Images) != 0 {
		t.Fatalf("result = %#v, want the text alone", result)
	}
}

// A Responses client returns an image inside a function output as an
// input_image part. It becomes the result's image; the text parts stay the
// text, and a part without a home keeps its JSON there too.
func TestOpenAIResponsesFunctionCallOutputImagesBecomeImages(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "view_image",
				"arguments": `{"path":"shot.png"}`,
			},
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "Attached image shot.png"},
					map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo=", "detail": "high"},
					map[string]interface{}{"type": "input_file", "file_id": "file_1"},
				},
			},
		},
	}

	typed, err := OpenAIResponsesRequestToTyped(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTyped() error = %v", err)
	}
	last := typed.Messages[len(typed.Messages)-1]
	block, ok := last.Blocks[0].(core.ToolResultBlock)
	if !ok || last.Role != string(core.RoleUser) {
		t.Fatalf("last message = %#v, want the tool result", last)
	}
	if block.Name != "view_image" {
		t.Fatalf("Name = %q, want the name of the call it answers", block.Name)
	}
	if want := "Attached image shot.png\n" + `{"file_id":"file_1","type":"input_file"}`; block.Content != want {
		t.Fatalf("Content = %q, want %q", block.Content, want)
	}
	if len(block.Images) != 1 || block.Images[0].URL != "data:image/png;base64,iVBORw0KGgo=" || block.Images[0].Detail != "high" {
		t.Fatalf("Images = %#v", block.Images)
	}
}

func TestOpenAIResponsesFunctionCallOutputImageOnlyHasEmptyText(t *testing.T) {
	text, images := responsesFunctionCallOutputParts([]interface{}{
		map[string]interface{}{"type": "input_image", "image_url": "https://example.test/shot.png"},
	})
	if text != "" || len(images) != 1 || images[0].URL != "https://example.test/shot.png" || images[0].Detail != "" {
		t.Fatalf("text = %q, images = %#v", text, images)
	}
}
