package core

import "testing"

func TestMemorySessionStoreWithUserBlocksKeepsImages(t *testing.T) {
	image := ImageBlock{URL: ImageDataURL("image/png", pngBytes), Name: "shot.png", Detail: "high"}
	store := NewMemorySessionStoreWithUserBlocks("system prompt", UserMessageBlocks("what is this?", []ImageBlock{image}))

	messages, err := store.Messages(store.GetPath())
	if err != nil {
		t.Fatalf("Messages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want system and user", len(messages))
	}
	user := messages[1]
	if user.Role != string(RoleUser) || len(user.Blocks) != 2 {
		t.Fatalf("user message = %#v, want image then text", user)
	}
	if got, ok := user.Blocks[0].(ImageBlock); !ok || got != image {
		t.Fatalf("Blocks[0] = %#v, want %#v", user.Blocks[0], image)
	}
	if got, ok := user.Blocks[1].(TextBlock); !ok || got.Text != "what is this?" {
		t.Fatalf("Blocks[1] = %#v, want the prompt", user.Blocks[1])
	}

	// The store hands out copies: a caller editing the slice does not edit
	// the conversation the next round is built from.
	messages[1].Blocks[0] = TextBlock{Text: "edited"}
	again, _ := store.Messages(store.GetPath())
	if _, ok := again[1].Blocks[0].(ImageBlock); !ok {
		t.Fatal("store contents changed through a returned copy")
	}
}

func TestMemorySessionStoreWithoutUserBlocksSeedsSystemOnly(t *testing.T) {
	for name, store := range map[string]*MemorySessionStore{
		"legacy constructor": NewMemorySessionStore("system prompt", ""),
		"block constructor":  NewMemorySessionStoreWithUserBlocks("system prompt", nil),
	} {
		messages, err := store.Messages(store.GetPath())
		if err != nil {
			t.Fatalf("%s: Messages failed: %v", name, err)
		}
		if len(messages) != 1 || messages[0].Role != string(RoleSystem) {
			t.Fatalf("%s: got %#v, want the system message alone", name, messages)
		}
	}
}
