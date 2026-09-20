package core

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

const toolResultRenderText = "Attached image shot.png (image/png, 12 bytes)."

// toolResultRound is one round with an image result: the prompt, the
// assistant's view_image call, and the user message carrying the result and,
// when note is non-empty, the trailing note.
func toolResultRound(images []ImageBlock, note string) []TypedMessage {
	blocks := []Block{ToolResultBlock{ToolUseID: "img", Name: ViewImageToolName, Content: toolResultRenderText, Images: images}}
	if note != "" {
		blocks = append(blocks, TextBlock{Text: note})
	}
	return []TypedMessage{
		NewTextMessage(string(RoleUser), "look at shot.png"),
		{Role: string(RoleAssistant), Blocks: []Block{ToolUseBlock{ID: "img", Name: ViewImageToolName, Input: json.RawMessage(`{"path":"shot.png"}`)}}},
		{Role: string(RoleUser), Blocks: blocks},
	}
}

func pngBase64() string {
	return base64.StdEncoding.EncodeToString(pngBytes)
}

func asMap(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	m, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("value type = %T, want map: %#v", value, value)
	}
	return m
}

func TestAnthropicToolResultWithImageIsAContentArray(t *testing.T) {
	image := toolResultTestImage()
	messages := ToAnthropicTyped(toolResultRound([]ImageBlock{image}, "Note: x"))
	last := messages[len(messages)-1]
	if last.Role != "user" || len(last.Content.Contents) != 2 {
		t.Fatalf("last message = %#v, want the result and the note", last)
	}
	result := last.Content.Contents[0]
	if result.Type != "tool_result" || result.ToolUseID != "img" || result.Content != "" || len(result.ContentBlocks) != 2 {
		t.Fatalf("tool_result = %#v, want the array form", result)
	}

	rendered := MarshalAnthropicMessagesForRequest(messages)
	content, ok := asMap(t, rendered[2])["content"].([]interface{})
	if !ok {
		t.Fatalf("content type = %T", asMap(t, rendered[2])["content"])
	}
	parts, ok := asMap(t, content[0])["content"].([]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("tool_result content = %#v, want a text and an image block", asMap(t, content[0])["content"])
	}
	text := asMap(t, parts[0])
	if text["type"] != "text" || text["text"] != toolResultRenderText {
		t.Fatalf("parts[0] = %#v", text)
	}
	imagePart := asMap(t, parts[1])
	source := asMap(t, imagePart["source"])
	if imagePart["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != pngBase64() {
		t.Fatalf("parts[1] = %#v", imagePart)
	}
	if note := asMap(t, content[1]); note["type"] != "text" || note["text"] != "Note: x" {
		t.Fatalf("content[1] = %#v, want the note after the result", note)
	}
}

func TestAnthropicToolResultWithoutImagesStaysAString(t *testing.T) {
	rendered := MarshalAnthropicMessagesForRequest(ToAnthropicTyped(toolResultRound(nil, "")))
	content := asMap(t, rendered[2])["content"].([]interface{})
	if got := asMap(t, content[0])["content"]; got != toolResultRenderText {
		t.Fatalf("tool_result content = %#v, want the plain string", got)
	}
}

func TestFromAnthropicTypedReadsToolResultImagesBack(t *testing.T) {
	image := toolResultTestImage()
	typed := FromAnthropicTyped(ToAnthropicTyped(toolResultRound([]ImageBlock{image}, "")))
	last := typed[len(typed)-1]
	block, ok := last.Blocks[0].(ToolResultBlock)
	if !ok || block.ToolUseID != "img" || block.Content != toolResultRenderText {
		t.Fatalf("block = %#v", last.Blocks[0])
	}
	if len(block.Images) != 1 || block.Images[0].URL != image.URL || block.Images[0].Detail != "auto" {
		t.Fatalf("Images = %#v, want the data URL back", block.Images)
	}
}

func TestOpenAIResponsesFunctionOutputWithImageIsAContentList(t *testing.T) {
	image := toolResultTestImage()
	var output map[string]interface{}
	for _, raw := range OpenAIResponsesInput(toolResultRound([]ImageBlock{image}, "Note: x")) {
		if item := asMap(t, raw); item["type"] == "function_call_output" {
			output = item
		}
	}
	if output == nil {
		t.Fatal("no function_call_output item")
	}
	parts, ok := output["output"].([]map[string]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("output = %#v, want a text and an image part", output["output"])
	}
	if parts[0]["type"] != "input_text" || parts[0]["text"] != toolResultRenderText {
		t.Fatalf("parts[0] = %#v", parts[0])
	}
	if parts[1]["type"] != "input_image" || parts[1]["image_url"] != image.URL || parts[1]["detail"] != "low" {
		t.Fatalf("parts[1] = %#v", parts[1])
	}
}

func TestOpenAIResponsesFunctionOutputWithoutImagesStaysAString(t *testing.T) {
	item := openAIResponsesToolCallOutputItem(ToolResultBlock{ToolUseID: "c", Content: "raw"})
	if item["type"] != "function_call_output" || item["output"] != "raw" {
		t.Fatalf("item = %#v", item)
	}
	custom := openAIResponsesToolCallOutputItem(ToolResultBlock{ToolUseID: "c", Type: "custom", Content: "raw"})
	if custom["type"] != "custom_tool_call_output" || custom["output"] != "raw" {
		t.Fatalf("custom item = %#v", custom)
	}
}

func TestOpenAIChatToolResultImageFollowsInAUserMessage(t *testing.T) {
	image := toolResultTestImage()
	messages := ToOpenAITyped(toolResultRound([]ImageBlock{image}, "Note: x"))
	if len(messages) != 4 {
		t.Fatalf("got %d messages, want user, assistant, tool, user: %#v", len(messages), messages)
	}
	tool := messages[2]
	if tool.Role != "tool" || tool.ToolCallID != "img" || tool.Content.Text == nil || *tool.Content.Text != toolResultRenderText {
		t.Fatalf("tool message = %#v", tool)
	}
	user := messages[3]
	if user.Role != "user" || len(user.Content.Contents) != 2 {
		t.Fatalf("user message = %#v, want the image then the note", user)
	}
	if part := user.Content.Contents[0]; part.Type != "image_url" || part.ImageURL == nil || part.ImageURL.URL != image.URL || part.ImageURL.Detail != "low" {
		t.Fatalf("Contents[0] = %#v", part)
	}
	if part := user.Content.Contents[1]; part.Type != "text" || part.Text != "Note: x" {
		t.Fatalf("Contents[1] = %#v", part)
	}

	rendered := MarshalOpenAIMessagesForRequest(messages)
	content, ok := asMap(t, rendered[3])["content"].([]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("rendered user content = %#v", asMap(t, rendered[3])["content"])
	}
	imageURL := asMap(t, asMap(t, content[0])["image_url"])
	if imageURL["url"] != image.URL || imageURL["detail"] != "low" {
		t.Fatalf("image_url = %#v", imageURL)
	}
}

func TestOpenAIChatToolResultImageOnlyRoundHasNoTextPart(t *testing.T) {
	messages := ToOpenAITyped(toolResultRound([]ImageBlock{toolResultTestImage()}, ""))
	if len(messages) != 4 || messages[3].Role != "user" || len(messages[3].Content.Contents) != 1 {
		t.Fatalf("messages = %#v, want a trailing user message holding the image alone", messages)
	}
	if messages[3].Content.Contents[0].Type != "image_url" {
		t.Fatalf("Contents[0] = %#v", messages[3].Content.Contents[0])
	}
}

func TestOpenAIChatToolResultsWithoutImagesAreUnchanged(t *testing.T) {
	withNote := ToOpenAITyped(toolResultRound(nil, "Note: x"))
	if len(withNote) != 4 || withNote[3].Role != "user" || withNote[3].Content.Contents != nil || withNote[3].Content.Text == nil || *withNote[3].Content.Text != "Note: x" {
		t.Fatalf("messages = %#v, want the note as a plain user message", withNote)
	}
	if plain := ToOpenAITyped(toolResultRound(nil, "")); len(plain) != 3 || plain[2].Role != "tool" {
		t.Fatalf("messages = %#v, want the tool message last", plain)
	}
}

func TestGoogleToolResultImageNestsUnderTheFunctionResponse(t *testing.T) {
	image := toolResultTestImage()
	messages := ToGoogleTyped(toolResultRound([]ImageBlock{image}, "Note: x"))
	last := messages[len(messages)-1]
	if last.Role != "user" || len(last.Parts) != 2 {
		t.Fatalf("last message = %#v, want the function response and the note", last)
	}
	for _, part := range last.Parts {
		if part.InlineData != nil {
			t.Fatal("the image was rendered as a sibling part, which Gemini 3 rejects")
		}
	}
	response := last.Parts[0].FunctionResponse
	if response == nil || response.Name != ViewImageToolName || response.Response.Content != toolResultRenderText {
		t.Fatalf("functionResponse = %#v", response)
	}
	if len(response.Parts) != 1 || response.Parts[0].InlineData == nil || response.Parts[0].InlineData.MimeType != "image/png" || response.Parts[0].InlineData.Data != pngBase64() {
		t.Fatalf("nested parts = %#v, want one inlineData part", response.Parts)
	}

	rendered := MarshalGoogleMessagesForRequest(messages)
	parts := asMap(t, rendered[len(rendered)-1])["parts"].([]map[string]interface{})
	responseMap := asMap(t, parts[0]["functionResponse"])
	nested, ok := responseMap["parts"].([]map[string]interface{})
	if !ok || len(nested) != 1 {
		t.Fatalf("functionResponse.parts = %#v", responseMap["parts"])
	}
	inline := asMap(t, nested[0]["inlineData"])
	if inline["mimeType"] != "image/png" || inline["data"] != pngBase64() {
		t.Fatalf("inlineData = %#v", inline)
	}
	if _, present := parts[1]["functionResponse"]; present || parts[1]["text"] != "Note: x" {
		t.Fatalf("parts[1] = %#v, want the note text", parts[1])
	}
}

func TestGoogleToolResultURLImageBecomesAMention(t *testing.T) {
	messages := ToGoogleTyped(toolResultRound([]ImageBlock{{URL: "https://example.test/shot.png"}}, ""))
	response := messages[len(messages)-1].Parts[0].FunctionResponse
	if len(response.Parts) != 0 {
		t.Fatalf("nested parts = %#v, want none for a URL image", response.Parts)
	}
	if want := toolResultRenderText + "\n[Image: https://example.test/shot.png]"; response.Response.Content != want {
		t.Fatalf("content = %q, want %q", response.Response.Content, want)
	}
}

func TestGoogleToolResultWithoutImagesHasNoNestedParts(t *testing.T) {
	rendered := MarshalGoogleMessagesForRequest(ToGoogleTyped(toolResultRound(nil, "")))
	parts := asMap(t, rendered[len(rendered)-1])["parts"].([]map[string]interface{})
	if _, present := asMap(t, parts[0]["functionResponse"])["parts"]; present {
		t.Fatalf("functionResponse = %#v, want no parts key", parts[0]["functionResponse"])
	}
}
