package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// saveViewImageRound commits an assistant view_image call and the result
// that answers it, and returns the result message's ID.
func saveViewImageRound(t *testing.T, ctx context.Context, sess *Session, image core.ImageBlock) string {
	t.Helper()
	call := core.ToolCall{ID: "img-1", Name: core.ViewImageToolName, Args: json.RawMessage(`{"path":"shot.png"}`)}
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{
		Role:      core.RoleAssistant,
		Timestamp: time.Now(),
		Model:     "test-model",
	}, []core.ToolCall{call}, nil); err != nil {
		t.Fatalf("AppendMessageWithToolInteraction() error = %v", err)
	}
	result, err := SaveToolResults(ctx, sess, []core.ToolResult{{
		ID:      "img-1",
		Output:  "Attached image " + core.DescribeImageBlock(image) + ".",
		Elapsed: 1,
		Images:  []core.ImageBlock{image},
	}}, "")
	if err != nil {
		t.Fatalf("SaveToolResults() error = %v", err)
	}
	return result.MessageID
}

func TestToolResultImageIsStoredOnceInTheBlocksFile(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess, err := CreateSession("system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	image := sessionTestImage()
	msgID := saveViewImageRound(t, ctx, sess, image)
	paths := buildMessageFilePaths(sess.Path, msgID)

	toolsJSON, err := os.ReadFile(paths.ToolsPath)
	if err != nil {
		t.Fatalf("read .tools.json: %v", err)
	}
	for _, unwanted := range []string{"base64,", `"images"`} {
		if strings.Contains(string(toolsJSON), unwanted) {
			t.Fatalf(".tools.json holds %q; the bytes belong to .blocks.json alone", unwanted)
		}
	}
	if !strings.Contains(string(toolsJSON), "Attached image shot.png") {
		t.Fatalf(".tools.json = %s, want the result text", toolsJSON)
	}

	blocks, ok, err := loadMessageBlocks(sess.Path, msgID)
	if err != nil || !ok {
		t.Fatalf("loadMessageBlocks() = ok %v, err %v", ok, err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want the result alone", blocks)
	}
	result, isResult := blocks[0].(core.ToolResultBlock)
	if !isResult || result.ToolUseID != "img-1" || len(result.Images) != 1 || result.Images[0] != image {
		t.Fatalf("blocks[0] = %#v, want the result carrying %#v", blocks[0], image)
	}
	blocksJSON, err := os.ReadFile(paths.BlocksPath)
	if err != nil {
		t.Fatalf("read .blocks.json: %v", err)
	}
	if count := strings.Count(string(blocksJSON), "base64,"); count != 1 {
		t.Fatalf(".blocks.json holds the bytes %d times, want once", count)
	}
}

func TestToolResultImageIsReplayedWithItsToolName(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess, err := CreateSession("system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	image := sessionTestImage()
	saveViewImageRound(t, ctx, sess, image)

	replayed, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	last := lastMessage(t, replayed)
	result, ok := last.Blocks[0].(core.ToolResultBlock)
	if last.Role != string(core.RoleUser) || !ok {
		t.Fatalf("last message = %#v, want the tool-results user message", last)
	}
	if result.Name != core.ViewImageToolName {
		t.Fatalf("Name = %q, want the name resolved from the assistant call", result.Name)
	}
	if len(result.Images) != 1 || result.Images[0] != image {
		t.Fatalf("Images = %#v, want %#v", result.Images, image)
	}
	if !strings.HasPrefix(result.Content, "Attached image shot.png") {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestShowListsToolResultImagesUnderTheResult(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess, err := CreateSession("system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	msgID := saveViewImageRound(t, ctx, sess, sessionTestImage())
	notifier := core.NewTestNotifier()
	want := "     Attached image shot.png (image/png, 12 bytes).\n     [image: shot.png (image/png, 12 bytes)]\n"

	conversation, err := captureDisplayStdout(t, func() error { return ShowConversation(sess.Path, notifier) })
	if err != nil {
		t.Fatalf("ShowConversation() error = %v", err)
	}
	if !strings.Contains(conversation, want) {
		t.Fatalf("ShowConversation() output = %q, want it to contain %q", conversation, want)
	}
	if strings.Contains(conversation, "base64,") {
		t.Fatal("ShowConversation() printed image bytes")
	}

	single, err := captureDisplayStdout(t, func() error {
		return ShowMessageWithManager(DefaultManager(), filepath.Join(sess.Path, msgID), notifier)
	})
	if err != nil {
		t.Fatalf("ShowMessageWithManager() error = %v", err)
	}
	if !strings.Contains(single, want) {
		t.Fatalf("ShowMessageWithManager() output = %q, want it to contain %q", single, want)
	}
	if len(notifier.WarnMessages) != 0 {
		t.Fatalf("unexpected warnings: %v", notifier.WarnMessages)
	}
}
