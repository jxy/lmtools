package session

import (
	"context"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestIsMessageMetadataFilename(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "0000.json", want: true},
		{name: "10000.json", want: true},
		{name: "ffffffff.json", want: true},
		{name: "000.json", want: false},
		{name: "000000000.json", want: false},
		{name: "FFFF.json", want: false},
		{name: "000g.json", want: false},
		{name: ".tmp-123456.json", want: false},
		{name: "notes.json", want: false},
		{name: "0000.tools.json", want: false},
		{name: "0000.blocks.json", want: false},
		{name: "0000.txt", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMessageMetadataFilename(tt.name); got != tt.want {
				t.Fatalf("isMessageMetadataFilename(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestLineageIgnoresStagedMetadataFiles(t *testing.T) {
	tests := []struct {
		name       string
		stagedData []byte
	}{
		{name: "complete", stagedData: nil},
		{name: "empty", stagedData: []byte{}},
		{name: "partial", stagedData: []byte("{")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, _ := NewTestManager(t)
			sess, err := manager.CreateSession("", core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("CreateSession() error = %v", err)
			}

			committed := Message{
				ID:        "0000",
				Role:      core.RoleUser,
				Content:   "committed",
				Timestamp: time.Now(),
			}
			if err := writeMessage(sess.Path, committed.ID, committed); err != nil {
				t.Fatalf("writeMessage() error = %v", err)
			}

			staged, err := stageMessageFilesWithBlocks(sess.Path, Message{
				Role:      core.RoleAssistant,
				Content:   "not committed",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatalf("stageMessageFilesWithBlocks() error = %v", err)
			}
			defer staged.Close()
			if got := filepath.Base(staged.JsonPath); !isStagingMetadataFilenameForTest(got) {
				t.Fatalf("staged metadata filename = %q, want .tmp-*.json", got)
			}
			if tt.stagedData != nil {
				if err := os.WriteFile(staged.JsonPath, tt.stagedData, 0o600); err != nil {
					t.Fatalf("replace staged metadata: %v", err)
				}
			}

			refs, err := lineageMessageRefsWithManager(manager, sess.Path)
			if err != nil {
				t.Fatalf("lineageMessageRefsWithManager() error = %v", err)
			}
			if len(refs) != 1 || refs[0].message.ID != committed.ID {
				t.Fatalf("lineage message IDs = %v, want [%s]", messageRefIDsForTest(refs), committed.ID)
			}
		})
	}
}

func TestConversationSnapshotRefreshIgnoresStagedMetadata(t *testing.T) {
	manager, _ := NewTestManager(t)
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	committed := Message{
		ID:        "0000",
		Role:      core.RoleUser,
		Content:   "committed",
		Timestamp: time.Now(),
	}
	if err := writeMessage(sess.Path, committed.ID, committed); err != nil {
		t.Fatalf("writeMessage() error = %v", err)
	}

	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}
	staged, err := stageMessageFilesWithBlocks(sess.Path, Message{
		Role:      core.RoleAssistant,
		Content:   "not committed",
		Timestamp: time.Now(),
	}, nil, nil)
	if err != nil {
		t.Fatalf("stageMessageFilesWithBlocks() error = %v", err)
	}
	defer staged.Close()
	if err := os.WriteFile(staged.JsonPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("partially write staged metadata: %v", err)
	}

	messages, err := snapshot.buildTypedMessages(context.Background(), sess.Path)
	if err != nil {
		t.Fatalf("buildTypedMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("typed messages = %#v, want only committed message", messages)
	}
	if len(messages[0].Blocks) != 1 {
		t.Fatalf("typed message blocks = %#v, want one committed text block", messages[0].Blocks)
	}
	text, ok := messages[0].Blocks[0].(core.TextBlock)
	if !ok || text.Text != committed.Content {
		t.Fatalf("typed message blocks = %#v, want committed text %q", messages[0].Blocks, committed.Content)
	}
	if got, want := snapshot.activeMessageIDs, []string{committed.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active message IDs = %v, want %v", got, want)
	}
}

func isStagingMetadataFilenameForTest(name string) bool {
	return len(name) > len(".tmp-.json") && name[:5] == ".tmp-" && filepath.Ext(name) == ".json"
}

func messageRefIDsForTest(refs []lineageMessageRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.message.ID)
	}
	return ids
}
