package core

import (
	"testing"
)

func TestConvertBlocksToOpenAIContentTyped_TextOnly(t *testing.T) {
	blocks := []Block{TextBlock{Text: "Hello"}}
	content, toolCalls := ConvertBlocksToOpenAIContentTyped(blocks)

	if content.Text == nil || *content.Text != "Hello" {
		t.Errorf("expected text 'Hello', got %+v", content.Text)
	}
	if len(content.Contents) != 0 {
		t.Errorf("expected no multimodal contents, got %d", len(content.Contents))
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(toolCalls))
	}
}

func TestConvertBlocksToOpenAIContentTyped_Multimodal(t *testing.T) {
	blocks := []Block{
		TextBlock{Text: "See this"},
		ImageBlock{URL: "https://example.com/img.png", Detail: "high"},
	}
	content, toolCalls := ConvertBlocksToOpenAIContentTyped(blocks)

	if content.Text != nil {
		t.Errorf("expected Text=nil for multimodal, got %+v", *content.Text)
	}
	if len(content.Contents) != 2 {
		t.Fatalf("expected 2 content items, got %d", len(content.Contents))
	}
	if content.Contents[0].Type != "text" || content.Contents[0].Text != "See this" {
		t.Errorf("first content should be text 'See this', got %+v", content.Contents[0])
	}
	if content.Contents[1].Type != "image_url" || content.Contents[1].ImageURL == nil || content.Contents[1].ImageURL.URL != "https://example.com/img.png" || content.Contents[1].ImageURL.Detail != "high" {
		t.Errorf("second content should be image_url with URL+detail, got %+v", content.Contents[1])
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(toolCalls))
	}
}

func TestConvertBlocksToOpenAIContentTyped_EmptyBlocks(t *testing.T) {
	var blocks []Block
	content, toolCalls := ConvertBlocksToOpenAIContentTyped(blocks)

	if content.Text != nil || len(content.Contents) != 0 {
		t.Errorf("expected empty union (Text=nil, Contents=0), got %+v", content)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(toolCalls))
	}
}
