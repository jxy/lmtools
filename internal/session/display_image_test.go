package session

import (
	"io"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureDisplayStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout }()

	runErr := fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatalf("read stdout: %v", readErr)
	}
	return string(out), runErr
}

func TestShowListsImagesWithoutPrintingBytes(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess, err := CreateSession("system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	image := sessionTestImage()
	result, err := AppendMessageWithBlocks(ctx, sess, Message{
		Role:      core.RoleUser,
		Content:   "Look at this",
		Timestamp: time.Now(),
	}, nil, nil, core.UserMessageBlocks("Look at this", []core.ImageBlock{image}))
	if err != nil {
		t.Fatalf("AppendMessageWithBlocks() error = %v", err)
	}

	notifier := core.NewTestNotifier()
	wantLine := "[image: shot.png (image/png, 12 bytes)]"

	conversation, err := captureDisplayStdout(t, func() error { return ShowConversation(sess.Path, notifier) })
	if err != nil {
		t.Fatalf("ShowConversation() error = %v", err)
	}
	if !strings.Contains(conversation, "Look at this\n"+wantLine+"\n") {
		t.Fatalf("ShowConversation() output = %q, want the prompt followed by %q", conversation, wantLine)
	}
	if strings.Contains(conversation, "base64,") {
		t.Fatal("ShowConversation() printed image bytes")
	}

	single, err := captureDisplayStdout(t, func() error {
		return ShowMessageWithManager(DefaultManager(), filepath.Join(sess.Path, result.MessageID), notifier)
	})
	if err != nil {
		t.Fatalf("ShowMessageWithManager() error = %v", err)
	}
	if !strings.Contains(single, wantLine) {
		t.Fatalf("ShowMessageWithManager() output = %q, want %q", single, wantLine)
	}
	if len(notifier.WarnMessages) != 0 {
		t.Fatalf("unexpected warnings: %v", notifier.WarnMessages)
	}
}

func TestShowFindsImageOnlyMessageWithoutTextFile(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess, err := CreateSession("system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	image := sessionTestImage()
	result, err := AppendMessageWithBlocks(ctx, sess, Message{
		Role:      core.RoleUser,
		Timestamp: time.Now(),
	}, nil, nil, core.UserMessageBlocks("", []core.ImageBlock{image}))
	if err != nil {
		t.Fatalf("AppendMessageWithBlocks() error = %v", err)
	}
	paths := buildMessageFilePaths(sess.Path, result.MessageID)
	if fileExists(paths.TxtPath) {
		t.Fatal("image-only message wrote a .txt file; the test needs one without")
	}

	notifier := core.NewTestNotifier()
	ref := GetSessionID(sess.Path) + "/" + result.MessageID
	out, err := captureDisplayStdout(t, func() error { return ShowDispatcherWithManager(DefaultManager(), ref, notifier) })
	if err != nil {
		t.Fatalf("ShowDispatcherWithManager(%q) error = %v", ref, err)
	}
	if !strings.Contains(out, "[image: shot.png (image/png, 12 bytes)]") {
		t.Fatalf("output = %q, want the image line", out)
	}
	if !strings.Contains(out, "Type: user") {
		t.Fatalf("output = %q, want the message header", out)
	}
}
