//go:build !windows

package session

import (
	"encoding/json"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Deleting a message removes every one of its files, its image blocks
// included. An empty message committed later under the same ID has no
// sidecar of its own, and it shows no image.
func TestDeletingAMessageLeavesNoFileForItsIDToInherit(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system")
	imageID, err := lookAndAnswer(ctx, sess, taggedImage('A'))
	if err != nil {
		t.Fatalf("lookAndAnswer() error = %v", err)
	}
	if err := DeleteNode(filepath.Join(sess.Path, imageID)); err != nil {
		t.Fatalf("DeleteNode() error = %v", err)
	}
	paths := buildMessageFilePaths(sess.Path, imageID)
	for _, path := range []string{paths.JSONPath, paths.TxtPath, paths.ToolsPath, paths.BlocksPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s after the deletion: %v, want it gone", filepath.Base(path), err)
		}
	}

	empty, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleUser, Timestamp: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("append the empty message: %v", err)
	}
	if empty.MessageID != imageID {
		t.Fatalf("empty message is %s, want it at the deleted %s", empty.MessageID, imageID)
	}
	messages, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	if images := imagesIn(messages); len(images) != 0 {
		t.Fatalf("lineage shows %d images, want none once the image message is deleted", len(images))
	}
}

// A deletion by an older build, or one cut short, can leave a message's
// sidecars behind. A message committed later under that ID without files of
// those kinds clears them rather than read them as its own.
func TestCommitClearsSidecarsADeletedMessageLeftBehind(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system")
	imageID, err := lookAndAnswer(ctx, sess, taggedImage('A'))
	if err != nil {
		t.Fatalf("lookAndAnswer() error = %v", err)
	}
	paths := buildMessageFilePaths(sess.Path, imageID)
	leftovers := map[string][]byte{}
	for _, path := range []string{paths.TxtPath, paths.BlocksPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		leftovers[path] = data
	}
	tools, err := json.Marshal(core.ToolInteraction{Calls: []core.ToolCall{{ID: "stale", Name: "universal_command"}}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	leftovers[paths.ToolsPath] = tools
	if err := DeleteNode(filepath.Join(sess.Path, imageID)); err != nil {
		t.Fatalf("DeleteNode() error = %v", err)
	}
	for path, data := range leftovers {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("leave %s behind: %v", filepath.Base(path), err)
		}
	}

	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleUser, Timestamp: time.Now()}, nil, nil); err != nil {
		t.Fatalf("append the empty message: %v", err)
	}
	for path := range leftovers {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s after the commit: %v, want it cleared", filepath.Base(path), err)
		}
	}
	msg, err := readMessage(sess.Path, imageID)
	if err != nil || msg.Content != "" {
		t.Fatalf("empty message read as %+v, %v; want no text", msg, err)
	}
	if interaction, err := LoadToolInteraction(sess.Path, imageID); err != nil || interaction != nil {
		t.Fatalf("empty message tool interaction = %+v, %v; want none", interaction, err)
	}
	messages, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	if images := imagesIn(messages); len(images) != 0 {
		t.Fatalf("lineage shows %d images, want none", len(images))
	}
}
