package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShowSessionKeepsPinnedHeadAndFullFailedToolOutput(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	call := core.ToolCall{ID: "failed", Name: "universal_command", Args: json.RawMessage(`{"command":["example"]}`)}
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant, Timestamp: time.Now()}, []core.ToolCall{call}, nil); err != nil {
		t.Fatal(err)
	}
	output := strings.Repeat("a long line\n\n", 100) + "last byte"
	if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: output, Error: "exit status 1", Elapsed: 1234}}, ""); err != nil {
		t.Fatal(err)
	}
	pinned, err := OpenSession(ctx, GetSessionID(sess.Path))
	if err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "later answer")
	var out bytes.Buffer
	if err := ShowSessionTo(ctx, &out, pinned, core.NewTestNotifier()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"example": Failed in 1.2s: exit status 1`, "Partial output:\n" + output + "\n      End output."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in saved transcript %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "later answer") {
		t.Fatal("show included a message past the pinned head")
	}
	if kind, err := SavedHeadKind(ctx, pinned); err != nil || kind != "1 tool result(s)" {
		t.Fatalf("saved head=%q,%v", kind, err)
	}
	if kind, err := SavedHeadKind(ctx, sess); err != nil || kind != "assistant response" {
		t.Fatalf("latest head=%q,%v", kind, err)
	}
}

func TestConversationDisplayUsesEachMessagesDirectory(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	appendRound := func(sess *Session, label string) string {
		image := sessionTestImage()
		image.Name = label + "-user.png"
		text := label + " question"
		if _, err := AppendMessageWithBlocks(ctx, sess, Message{Role: core.RoleUser, Content: text, Timestamp: time.Now()}, nil, nil, core.UserMessageBlocks(text, []core.ImageBlock{image})); err != nil {
			t.Fatal(err)
		}
		call := core.ToolCall{ID: "reused-call-id", Name: core.UniversalCommandToolName, Args: json.RawMessage(fmt.Sprintf(`{"command":["%s-command"]}`, label))}
		if _, err := SaveAssistantResponseWithTools(ctx, sess, "", []core.ToolCall{call}, "test-model"); err != nil {
			t.Fatal(err)
		}
		image.Name = label + "-result.png"
		output := label + " output\n\n" + strings.Repeat("long output\n", 60) + label + " end"
		if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: output, Error: "exit status 1", Images: []core.ImageBlock{image}}}, ""); err != nil {
			t.Fatal(err)
		}
		return appendPlanMessage(t, ctx, sess, core.RoleAssistant, label+" answer")
	}
	branchAt := func(sess *Session, anchor, label string) *Session {
		// Construct an actual nested legacy branch; CreateSibling bubbles up.
		name, err := GetNextSiblingPath(sess.Path, anchor)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(sess.Path, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		branch := &Session{Path: path}
		appendPlanMessage(t, ctx, branch, core.RoleAssistant, label+" replacement")
		return branch
	}
	root := createPlanSession(t, "")
	rootEnd := appendRound(root, "root")
	branch := branchAt(root, rootEnd, "branch")
	branchEnd := appendRound(branch, "branch")
	nested := branchAt(branch, branchEnd, "nested")
	appendRound(nested, "nested")
	unrelated := branchAt(root, rootEnd, "unrelated")
	appendRound(unrelated, "unrelated")

	for _, test := range []struct {
		name   string
		sess   *Session
		labels []string
	}{
		{"root", root, []string{"root"}},
		{"branch", branch, []string{"root", "branch"}},
		{"nested", nested, []string{"root", "branch", "nested"}},
	} {
		for _, standalone := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/standalone=%v", test.name, standalone), func(t *testing.T) {
				var out bytes.Buffer
				var err error
				if standalone {
					err = ShowConversationTo(&out, test.sess.Path, core.NewTestNotifier())
				} else {
					err = ShowSessionTo(ctx, &out, test.sess, core.NewTestNotifier())
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, label := range []string{"root", "branch", "nested", "unrelated"} {
					want := 0
					for _, included := range test.labels {
						if included == label {
							want = 1
						}
					}
					for _, marker := range []string{label + "-user.png", label + "-result.png", label + " output\n\n", label + " end\n", `"` + label + `-command": Failed`} {
						if count := strings.Count(out.String(), marker); count != want {
							t.Fatalf("%q appeared %d times, want %d:\n%s", marker, count, want, out.String())
						}
					}
				}
			})
		}
	}
}

func TestShowSessionRejectsReplacedHead(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	id := appendPlanMessage(t, ctx, sess, core.RoleUser, "original")
	pinned, err := OpenSession(ctx, GetSessionID(sess.Path))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(buildMessageFilePaths(sess.Path, id).JSONPath); err != nil {
		t.Fatal(err)
	}
	appendPlanMessage(t, ctx, sess, core.RoleUser, "replacement")
	var out bytes.Buffer
	if err := ShowSessionTo(ctx, &out, pinned, core.NewTestNotifier()); !errors.Is(err, ErrHeadReplaced) {
		t.Fatalf("replaced head: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("displayed replaced history: %q", out.String())
	}
}
