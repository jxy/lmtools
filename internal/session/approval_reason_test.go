//go:build !windows

package session

import (
	"context"
	"errors"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestApprovalDenialReasonsPersistAndReplayFromJournal(t *testing.T) {
	const reason = "use the saved report\nAvoid café credentials."
	const attribution = "user denied permission; reason provided by the user: " + reason
	for _, uncertain := range []bool{false, true} {
		name := "new execution"
		if uncertain {
			name = "uncertain rerun"
		}
		t.Run(name, func(t *testing.T) {
			UseTestSessionDir(t)
			ctx := context.Background()
			marker := filepath.Join(t.TempDir(), "ran")
			call := touchCall("call_1", core.NewInvocationID(), marker)
			claim, err := NewToolJournal().Claim([]core.ToolCall{call})
			if err != nil {
				t.Fatal(err)
			}
			if uncertain {
				if err := claim.Started(call); err != nil {
					t.Fatal(err)
				}
			}
			claim.Release()
			sess := pendingCallSession(t, []core.ToolCall{call})
			head, _, err := LastMessageRefWithManager(DefaultManager(), sess.Path)
			if err != nil {
				t.Fatal(err)
			}
			copied, err := ForkThroughHead(ctx, DefaultManager(), sess.Path, head)
			if err != nil {
				t.Fatal(err)
			}
			approver := core.NewTestApprover(false)
			approver.DefaultReason = reason
			approver.RerunReasons = []string{reason}
			resolution := resolveForTest(t, ctx, sess, pendingToolTestConfig(), core.NewTestNotifier(), approver)
			if !resolution.Committed || exists(marker) {
				t.Fatalf("resolution=%#v ran=%v", resolution, exists(marker))
			}
			result := lastToolResults(t, sess)[0]
			if result.Reason != attribution || !strings.Contains(result.Error, attribution) || result.NotRun == uncertain {
				t.Fatalf("stored result=%#v, want attributed denial with uncertain=%v", result, uncertain)
			}
			wantCode := lmerrors.ErrCodeNotApproved
			if uncertain {
				wantCode = lmerrors.ErrCodeOutcomeUnknown
				if !strings.Contains(result.Error, "outcome unknown: an earlier run started it and recorded no outcome; it was not run again") {
					t.Fatalf("original unknown outcome lost: %q", result.Error)
				}
			}
			if result.Code != wantCode {
				t.Fatalf("code=%q, want %q", result.Code, wantCode)
			}
			messages, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
			if err != nil {
				t.Fatal(err)
			}
			block := messages[len(messages)-1].Blocks[0].(core.ToolResultBlock)
			if !block.IsError || !strings.Contains(block.Content, attribution) || strings.Contains(block.Content, "Command was not executed.") == uncertain {
				t.Fatalf("replayed block=%#v, want attributed denial with uncertain=%v", block, uncertain)
			}
			state, journalResult, err := openJournal(nil).state(call.InvocationID)
			if err != nil || state != invocationKnown || !reflect.DeepEqual(journalResult, result) {
				t.Fatalf("journal state=%v result=%#v error=%v", state, journalResult, err)
			}
			// A copy with pending calls must reuse the journal's complete result,
			// including the denial reason, without asking or running again.
			replayApprover := core.NewTestApprover(true)
			replayApprover.RerunResponses = []bool{true}
			resolveForTest(t, ctx, copied, pendingToolTestConfig(), core.NewTestNotifier(), replayApprover)
			if exists(marker) || len(replayApprover.ApprovalCalls) != 0 || len(replayApprover.RerunCalls) != 0 || !reflect.DeepEqual(lastToolResults(t, copied)[0], result) {
				t.Fatal("journal replay changed the denial, asked again, or ran the call")
			}
		})
	}
}

type rerunAnswerApprover struct {
	*core.TestApprover
	answer core.Approval
	err    error
	cancel context.CancelFunc
}

func (a rerunAnswerApprover) ApproveRerun(context.Context, core.ToolCall) (core.Approval, error) {
	if a.cancel != nil {
		a.cancel()
	}
	return a.answer, a.err
}

func TestRerunReasonCannotOverridePromptErrorsOrCancellation(t *testing.T) {
	for _, cancelPrompt := range []bool{false, true} {
		t.Run(map[bool]string{false: "prompt error", true: "cancelled prompt"}[cancelPrompt], func(t *testing.T) {
			UseTestSessionDir(t)
			marker := filepath.Join(t.TempDir(), "ran")
			call := touchCall("call_1", core.NewInvocationID(), marker)
			claim, err := NewToolJournal().Claim([]core.ToolCall{call})
			if err != nil {
				t.Fatal(err)
			}
			if err := claim.Started(call); err != nil {
				t.Fatal(err)
			}
			claim.Release()
			sess := pendingCallSession(t, []core.ToolCall{call})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			promptError := errors.New("input failed")
			approver := rerunAnswerApprover{TestApprover: core.NewTestApprover(true), answer: core.Approval{Approved: true, Reason: "must be ignored"}, err: promptError}
			if cancelPrompt {
				approver.cancel = cancel
			}
			resolution, err := ResolvePendingToolCalls(ctx, sess, pendingToolTestConfig(), core.NewTestLogger(false), core.NewTestNotifier(), core.TestToolUI{}, approver)
			if err == nil || resolution.Committed || exists(marker) {
				t.Fatalf("resolution=%#v error=%v ran=%v", resolution, err, exists(marker))
			}
			if cancelPrompt && !errors.Is(err, context.Canceled) || !cancelPrompt && !errors.Is(err, promptError) {
				t.Fatalf("wrong prompt error: %v", err)
			}
			state, _, err := openJournal(nil).state(call.InvocationID)
			if err != nil || state != invocationStartedUnfinished {
				t.Fatalf("journal state=%v error=%v, want uncertain outcome retained", state, err)
			}
		})
	}
}
