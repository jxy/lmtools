package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func imageTestUserTurn() ([]TypedMessage, ImageBlock, string) {
	image := ImageBlock{URL: ImageDataURL("image/png", pngBytes), Name: "shot.png", Detail: "high"}
	return []TypedMessage{NewUserMessage("what is this?", []ImageBlock{image})}, image, "what is this?"
}

func lastUserContent(t *testing.T, rendered interface{}) []interface{} {
	t.Helper()
	messages, ok := rendered.([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("rendered messages = %#v, want a non-empty list", rendered)
	}
	msg, ok := messages[len(messages)-1].(map[string]interface{})
	if !ok {
		t.Fatalf("last message = %#v, want an object", messages[len(messages)-1])
	}
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("content = %#v, want an array once an image is present", msg["content"])
	}
	return content
}

func TestAnthropicRendersDataURLImageAsBase64Source(t *testing.T) {
	messages, image, prompt := imageTestUserTurn()
	content := lastUserContent(t, imageTestJSON(t, MarshalAnthropicMessagesForRequest(ToAnthropicTyped(messages))))
	if len(content) != 2 {
		t.Fatalf("content = %#v, want image then text", content)
	}

	imagePart := content[0].(map[string]interface{})
	if imagePart["type"] != "image" {
		t.Fatalf("content[0].type = %v, want image", imagePart["type"])
	}
	source := imagePart["source"].(map[string]interface{})
	_, wantData, _ := ParseBase64DataURL(image.URL)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != wantData {
		t.Fatalf("source = %#v, want base64 image/png with the payload", source)
	}
	if _, has := source["url"]; has {
		t.Fatalf("source carries url alongside base64 data: %#v", source)
	}
	if _, has := imagePart["name"]; has {
		t.Fatalf("image part leaks the attachment name: %#v", imagePart)
	}

	textPart := content[1].(map[string]interface{})
	if textPart["type"] != "text" || textPart["text"] != prompt {
		t.Fatalf("content[1] = %#v, want the prompt text", textPart)
	}
}

func TestOpenAIChatRendersDataURLImagePart(t *testing.T) {
	messages, image, prompt := imageTestUserTurn()
	content := lastUserContent(t, imageTestJSON(t, MarshalOpenAIMessagesForRequest(ToOpenAITyped(messages))))
	if len(content) != 2 {
		t.Fatalf("content = %#v, want image then text", content)
	}

	imagePart := content[0].(map[string]interface{})
	if imagePart["type"] != "image_url" {
		t.Fatalf("content[0].type = %v, want image_url", imagePart["type"])
	}
	imageURL := imagePart["image_url"].(map[string]interface{})
	if imageURL["url"] != image.URL || imageURL["detail"] != "high" {
		t.Fatalf("image_url = %#v, want the data URL with detail high", imageURL)
	}

	textPart := content[1].(map[string]interface{})
	if textPart["type"] != "text" || textPart["text"] != prompt {
		t.Fatalf("content[1] = %#v, want the prompt text", textPart)
	}
}

func TestOpenAIResponsesRendersInputImagePart(t *testing.T) {
	messages, image, prompt := imageTestUserTurn()
	content := lastUserContent(t, imageTestJSON(t, OpenAIResponsesInput(messages)))
	if len(content) != 2 {
		t.Fatalf("content = %#v, want image then text", content)
	}

	imagePart := content[0].(map[string]interface{})
	if imagePart["type"] != "input_image" || imagePart["image_url"] != image.URL || imagePart["detail"] != "high" {
		t.Fatalf("content[0] = %#v, want input_image with the data URL and detail", imagePart)
	}
	textPart := content[1].(map[string]interface{})
	if textPart["type"] != "input_text" || textPart["text"] != prompt {
		t.Fatalf("content[1] = %#v, want input_text with the prompt", textPart)
	}
}

func TestBuildRequestCarriesImagesIntoUserTurn(t *testing.T) {
	image := ImageBlock{URL: ImageDataURL("image/png", pngBytes), Name: "shot.png"}
	cfg := RequestOptions{
		Provider: "openai",
		Model:    "gpt-test",
		System:   "system prompt",
		Images:   []ImageBlock{image},
	}

	_, body, err := BuildRequest(cfg, "what is this?")
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	content := lastUserContent(t, payload["messages"])
	if len(content) != 2 {
		t.Fatalf("content = %#v, want image then text", content)
	}
	imageURL := content[0].(map[string]interface{})["image_url"].(map[string]interface{})
	if imageURL["url"] != image.URL {
		t.Fatalf("image_url.url = %v, want the data URL", imageURL["url"])
	}
	if _, has := imageURL["detail"]; has {
		t.Fatalf("detail rendered without -image-detail: %#v", imageURL)
	}
	if text := content[1].(map[string]interface{})["text"]; text != "what is this?" {
		t.Fatalf("text = %v, want the prompt", text)
	}
	if strings.Contains(string(body), "shot.png") {
		t.Fatal("request body leaks the attachment name")
	}
}

func TestBuildRequestAcceptsImageOnlyInput(t *testing.T) {
	image := ImageBlock{URL: ImageDataURL("image/png", pngBytes)}
	cfg := RequestOptions{Provider: "anthropic", Model: "claude-test", System: "system prompt", Images: []ImageBlock{image}}

	_, body, err := BuildRequest(cfg, "")
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	content := lastUserContent(t, payload["messages"])
	if len(content) != 1 || content[0].(map[string]interface{})["type"] != "image" {
		t.Fatalf("content = %#v, want the image alone", content)
	}
}

func TestBuildRequestWithoutInputOrImagesSendsNoUserTurn(t *testing.T) {
	cfg := RequestOptions{Provider: "openai", Model: "gpt-test", System: "system prompt"}
	_, body, err := BuildRequest(cfg, "")
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	for _, raw := range payload["messages"].([]interface{}) {
		if raw.(map[string]interface{})["role"] == "user" {
			t.Fatalf("user turn rendered with nothing to send: %#v", raw)
		}
	}
}
