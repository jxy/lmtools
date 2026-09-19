package core

import "testing"

func TestFromAnthropicTypedBase64ImageSourceBecomesDataURL(t *testing.T) {
	messages := FromAnthropicTyped([]AnthropicMessage{{
		Role: "user",
		Content: AnthropicContentUnion{Contents: []AnthropicContent{
			{Type: "image", Source: &AnthropicImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}},
			{Type: "image", Source: &AnthropicImageSource{Type: "url", URL: "https://example.com/a.png"}},
			{Type: "text", Text: "what are these?"},
		}},
	}})
	if len(messages) != 1 || len(messages[0].Blocks) != 3 {
		t.Fatalf("FromAnthropicTyped() = %#v, want one message with three blocks", messages)
	}
	if got, ok := messages[0].Blocks[0].(ImageBlock); !ok || got.URL != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("Blocks[0] = %#v, want a data URL image", messages[0].Blocks[0])
	}
	if got, ok := messages[0].Blocks[1].(ImageBlock); !ok || got.URL != "https://example.com/a.png" {
		t.Fatalf("Blocks[1] = %#v, want the URL image", messages[0].Blocks[1])
	}
}

func TestAnthropicImageSourceURL(t *testing.T) {
	tests := []struct {
		name                            string
		sourceType, url, mediaType, raw string
		want                            string
	}{
		{name: "url source", sourceType: "url", url: "https://example.com/a.png", want: "https://example.com/a.png"},
		{name: "base64 source", sourceType: "base64", mediaType: "image/jpeg", raw: "AAAA", want: "data:image/jpeg;base64,AAAA"},
		{name: "untyped source with data", mediaType: "image/gif", raw: "AAAA", want: "data:image/gif;base64,AAAA"},
		{name: "empty source", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnthropicImageSourceURL(tt.sourceType, tt.url, tt.mediaType, tt.raw); got != tt.want {
				t.Fatalf("AnthropicImageSourceURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
