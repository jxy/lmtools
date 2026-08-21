package session

import (
	"context"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
				t.Fatalf("lineage message IDs = %v, want [%s]", lineageRefIDs(refs), committed.ID)
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
	if got, want := snapshot.activeIDs, []string{committed.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active message IDs = %v, want %v", got, want)
	}
}

func isStagingMetadataFilenameForTest(name string) bool {
	return len(name) > len(".tmp-.json") && strings.HasPrefix(name, ".tmp-") && filepath.Ext(name) == ".json"
}

// TestCorruptCommittedMessageIsSkippedRatherThanFailingTheLoad covers the state
// power loss leaves behind: commitFiles renames a temp into place without a
// prior sync, so a committed metadata file can be zero length. One of those
// must cost one message, not the session.
func TestCorruptCommittedMessageIsSkippedRatherThanFailingTheLoad(t *testing.T) {
	corruptions := map[string][]byte{
		"zero length": {},
		"truncated":   []byte("{"),
	}

	for name, corruption := range corruptions {
		t.Run(name, func(t *testing.T) {
			manager, _ := NewTestManager(t)
			ctx := context.Background()
			sess, err := manager.CreateSession("", core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("CreateSession() error = %v", err)
			}

			first := appendPlanMessage(t, ctx, sess, core.RoleUser, "first")
			corrupt := appendPlanMessage(t, ctx, sess, core.RoleAssistant, "lost to a crash")
			last := appendPlanMessage(t, ctx, sess, core.RoleUser, "last")
			if err := os.WriteFile(buildMessageFilePaths(sess.Path, corrupt).JSONPath, corruption, 0o600); err != nil {
				t.Fatalf("corrupt committed metadata: %v", err)
			}

			// The staged-temp rule is separate and still applies: a half-written
			// staging file in the same directory is not a message to begin with.
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

			refs, err := lineageMessageRefsWithManager(manager, sess.Path)
			if err != nil {
				t.Fatalf("lineageMessageRefsWithManager() error = %v", err)
			}
			if got, want := lineageRefIDs(refs), []string{first, last}; !reflect.DeepEqual(got, want) {
				t.Fatalf("lineage message IDs = %v, want %v", got, want)
			}

			// The scan still records the ID it skipped, so append detection does
			// not rediscover it every round.
			scan, err := scanLineage(manager, sess.Path)
			if err != nil {
				t.Fatalf("scanLineage() error = %v", err)
			}
			if got, want := scan.activeIDs, []string{first, corrupt, last}; !reflect.DeepEqual(got, want) {
				t.Fatalf("scanned active IDs = %v, want %v", got, want)
			}

			if _, err := pendingToolCallsFromLineageRefs(ctx, refs); err != nil {
				t.Fatalf("pendingToolCallsFromLineageRefs() error = %v", err)
			}

			messages, err := BuildMessagesWithToolInteractionsWithManager(ctx, manager, sess.Path)
			if err != nil {
				t.Fatalf("BuildMessagesWithToolInteractionsWithManager() error = %v", err)
			}
			if len(messages) != 2 {
				t.Fatalf("typed message count = %d, want the two readable messages", len(messages))
			}
		})
	}
}

// TestCorruptTrailingCommittedMessageStillResumes is the case a crash actually
// produces: the message being written when the power went out is the last one.
// The lineage scan skipping it is not enough on its own — refresh must know the
// scan already considered that ID, or it reads it as an append and fails there.
func TestCorruptTrailingCommittedMessageStillResumes(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	kept := appendPlanMessage(t, ctx, sess, core.RoleUser, "kept")
	trailing := appendPlanMessage(t, ctx, sess, core.RoleAssistant, "interrupted")
	if err := os.WriteFile(buildMessageFilePaths(sess.Path, trailing).JSONPath, nil, 0o600); err != nil {
		t.Fatalf("truncate trailing metadata: %v", err)
	}

	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}
	messages, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("buildTypedMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("typed message count = %d, want only %s", len(messages), kept)
	}

	// The next round must not rediscover the skipped trailing ID as an append.
	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("repeated buildTypedMessages() error = %v", err)
	}
	if got, want := lineageRefIDs(snapshot.refs), []string{kept}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after repeated refresh = %v, want %v", got, want)
	}

	// A message committed after the damaged one is still picked up.
	recovered := appendPlanMessage(t, ctx, sess, core.RoleUser, "resumed")
	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("buildTypedMessages() after append error = %v", err)
	}
	if got, want := lineageRefIDs(snapshot.refs), []string{kept, recovered}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after append = %v, want %v", got, want)
	}
}
