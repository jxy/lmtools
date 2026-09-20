package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func toolResultTestImage() ImageBlock {
	return ImageBlock{URL: ImageDataURL("image/png", pngBytes), Detail: "low", Name: "shot.png"}
}

func TestToolResultBlockFromResultCarriesImages(t *testing.T) {
	image := toolResultTestImage()
	result := ToolResult{ID: "img", Output: "Attached image shot.png (image/png, 12 bytes).", Images: []ImageBlock{image}}

	block := ToolResultBlockFromResult(result, ViewImageToolName)

	if block.ToolUseID != "img" || block.Name != ViewImageToolName || block.Content != result.Output || block.IsError {
		t.Fatalf("block = %#v", block)
	}
	if len(block.Images) != 1 || block.Images[0] != image {
		t.Fatalf("Images = %#v, want %#v", block.Images, image)
	}
	block.Images[0].Name = "edited"
	if result.Images[0].Name != "shot.png" {
		t.Fatal("the block shares the result's image slice")
	}
}

func TestToolResultBlockFromResultWithoutImagesHasNone(t *testing.T) {
	if block := ToolResultBlockFromResult(ToolResult{ID: "c", Output: "hi"}, ""); block.Images != nil {
		t.Fatalf("Images = %#v, want nil", block.Images)
	}
}

func TestToolResultsMessageBlocksKeepsImagesInPlace(t *testing.T) {
	image := toolResultTestImage()
	blocks := ToolResultsMessageBlocks([]ToolResult{
		{ID: "cmd", Output: "hi\n"},
		{ID: "img", Output: "Attached image shot.png (image/png, 12 bytes).", Images: []ImageBlock{image}},
	}, "Note: x\n", map[string]string{"img": ViewImageToolName})

	if len(blocks) != 3 {
		t.Fatalf("blocks = %#v, want two results and the note", blocks)
	}
	command, ok := blocks[0].(ToolResultBlock)
	if !ok || command.ToolUseID != "cmd" || len(command.Images) != 0 {
		t.Fatalf("blocks[0] = %#v, want the command result without images", blocks[0])
	}
	view, ok := blocks[1].(ToolResultBlock)
	if !ok || view.Name != ViewImageToolName || len(view.Images) != 1 || view.Images[0] != image {
		t.Fatalf("blocks[1] = %#v, want the image result", blocks[1])
	}
	if note, ok := blocks[2].(TextBlock); !ok || note.Text != "Note: x\n" {
		t.Fatalf("blocks[2] = %#v, want the note", blocks[2])
	}
}

func TestMemorySessionStoreCarriesToolResultImages(t *testing.T) {
	image := toolResultTestImage()
	store := NewMemorySessionStore("system", "look at plot.png")
	ctx := context.Background()
	if _, _, err := store.SaveAssistant(ctx, "", []ToolCall{{ID: "img", Name: ViewImageToolName, Args: json.RawMessage(`{"path":"plot.png"}`)}}, "m"); err != nil {
		t.Fatalf("SaveAssistant() error = %v", err)
	}
	if _, _, err := store.SaveToolResults(ctx, []ToolResult{{ID: "img", Output: "Attached", Images: []ImageBlock{image}}}, ""); err != nil {
		t.Fatalf("SaveToolResults() error = %v", err)
	}

	messages, err := store.Messages(store.GetPath())
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	last := messages[len(messages)-1]
	block, ok := last.Blocks[0].(ToolResultBlock)
	if last.Role != string(RoleUser) || !ok {
		t.Fatalf("last message = %#v, want the tool-results user message", last)
	}
	if block.Name != ViewImageToolName || len(block.Images) != 1 || block.Images[0] != image {
		t.Fatalf("block = %#v, want the named result with its image", block)
	}
}

// The bytes live in the message blocks alone. A result serialized for
// .tools.json carries the description and nothing that decodes to an image.
func TestToolResultJSONLeavesImagesOut(t *testing.T) {
	data, err := json.Marshal(ToolResult{ID: "img", Output: "Attached", Images: []ImageBlock{toolResultTestImage()}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, unwanted := range []string{"base64", "images", "shot.png"} {
		if strings.Contains(string(data), unwanted) {
			t.Fatalf("ToolResult JSON %s contains %q", data, unwanted)
		}
	}
	var back ToolResult
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if back.Output != "Attached" || back.Images != nil {
		t.Fatalf("round trip = %#v", back)
	}
}
