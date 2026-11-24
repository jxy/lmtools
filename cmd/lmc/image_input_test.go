package main

import (
	"lmtools/internal/core"
	"testing"
)

func TestNoSessionStoreCarriesImagesIntoToolLoop(t *testing.T) {
	image := core.ImageBlock{URL: "data:image/png;base64,iVBORw0KGgo=", Name: "shot.png"}
	opts := core.NewTestRequestConfig()
	opts.EffectiveSystem = "tool system prompt"
	opts.Images = []core.ImageBlock{image}

	store, build := createToolStoreAndMessageBuilder(opts, nil, "describe this")
	messages, err := build(store.GetPath())
	if err != nil {
		t.Fatalf("message builder error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want system and user", len(messages))
	}
	user := messages[1]
	if user.Role != string(core.RoleUser) || len(user.Blocks) != 2 {
		t.Fatalf("user message = %#v, want image then text", user)
	}
	if got, ok := user.Blocks[0].(core.ImageBlock); !ok || got != image {
		t.Errorf("Blocks[0] = %#v, want %#v", user.Blocks[0], image)
	}
	if got, ok := user.Blocks[1].(core.TextBlock); !ok || got.Text != "describe this" {
		t.Errorf("Blocks[1] = %#v, want the prompt", user.Blocks[1])
	}
}
