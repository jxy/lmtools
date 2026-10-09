package core

import (
	"context"
	"errors"
	lmerrors "lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalDenialReasonKeepsDefaultAndAttributesUserText(t *testing.T) {
	for _, tt := range []struct{ reason, want string }{
		{"", "user denied permission"},
		{" \t\n", "user denied permission"},
		{"  use the saved report\nAvoid café credentials.  ", "user denied permission; reason provided by the user: use the saved report\nAvoid café credentials."},
	} {
		if got := (Approval{Reason: tt.reason}).DenialReason(); got != tt.want {
			t.Fatalf("DenialReason(%q)=%q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestCustomApprovalDenialsNeverRunCommandImageOrMCP(t *testing.T) {
	const reason = "use the saved report\nAvoid café credentials."
	const want = "user denied permission; reason provided by the user: " + reason
	marker := filepath.Join(t.TempDir(), "ran")
	image := writeTestImage(t, "private.png", pngBytes)
	tools := githubTools()
	approver := NewTestApprover(false)
	approver.DefaultReason = reason
	executor, err := NewExecutor(RequestOptions{ToolEnabled: true, MCP: tools}, nil, approver)
	if err != nil {
		t.Fatal(err)
	}
	calls := []ToolCall{
		commandCall("command", "touch", marker),
		viewImagePathCall(t, "image", ViewImageArgs{Path: image}),
		mcpToolCall("mcp", "mcp__github__create_issue", `{"title":"private"}`),
	}
	results := executor.ExecuteParallel(context.Background(), calls, TestToolUI{})
	if len(approver.ApprovalCalls) != 1 || len(approver.ImageApprovalCalls) != 1 || len(approver.MCPApprovalCalls) != 1 {
		t.Fatalf("questions: command=%d image=%d MCP=%d", len(approver.ApprovalCalls), len(approver.ImageApprovalCalls), len(approver.MCPApprovalCalls))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied command touched its file: %v", err)
	}
	if len(tools.recorded()) != 0 {
		t.Fatal("denied MCP tool was called")
	}
	for i, result := range results {
		if !result.NotRun || result.Code != lmerrors.ErrCodeNotApproved || result.Error != want || result.Reason != want {
			t.Fatalf("result[%d]=%#v, want attributed denial", i, result)
		}
		if len(result.Images) != 0 || result.Output != "" {
			t.Fatalf("denied tool carried output: %#v", result)
		}
		block := ToolResultBlockFromResult(result, calls[i].Name)
		if !block.IsError || block.Content != notRunStatement+"\n"+want {
			t.Fatalf("model block[%d]=%#v, want explicit user denial and reason", i, block)
		}
	}
}

type structuredAnswerApprover struct {
	DeclineNonCommandApprovals
	answer Approval
	err    error
	cancel context.CancelFunc
}

func (a structuredAnswerApprover) Approve(context.Context, UniversalCommandArgs) (Approval, error) {
	if a.cancel != nil {
		a.cancel()
	}
	return a.answer, a.err
}

func TestApprovalReasonsCannotOverrideApprovalErrorsOrCancellation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		answer    Approval
		err       error
		cancel    bool
		wantCode  string
		wantError string
		wantRun   bool
	}{
		{"approved reason ignored", Approval{Approved: true, Reason: "must be ignored"}, nil, false, "", "", true},
		{"error with approval", Approval{Approved: true, Reason: "must be ignored"}, errors.New("input failed"), false, lmerrors.ErrCodeApprovalError, "approval failed: input failed", false},
		{"error with denial", Approval{Reason: "must be ignored"}, errors.New("input failed"), false, lmerrors.ErrCodeApprovalError, "approval failed: input failed", false},
		{"cancelled prompt", Approval{Reason: "must be ignored"}, context.Canceled, true, lmerrors.ErrCodeCancelled, "execution cancelled", false},
		{"cancelled approval", Approval{Approved: true, Reason: "must be ignored"}, nil, true, lmerrors.ErrCodeCancelled, "execution cancelled", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			approver := structuredAnswerApprover{answer: tt.answer, err: tt.err}
			if tt.cancel {
				approver.cancel = cancel
			}
			executor, err := NewExecutor(RequestOptions{}, nil, approver)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "ran")
			result := singleResult(t, executor.ExecuteParallel(ctx, []ToolCall{commandCall("call_1", "touch", marker)}, TestToolUI{}))
			if result.Code != tt.wantCode || result.Error != tt.wantError || result.NotRun == tt.wantRun {
				t.Fatalf("result=%#v, want code %q error %q run=%v", result, tt.wantCode, tt.wantError, tt.wantRun)
			}
			if strings.Contains(result.Error, "must be ignored") || strings.Contains(result.Reason, "must be ignored") {
				t.Fatal("reason affected an approval error or a successful approval")
			}
			_, err = os.Stat(marker)
			if (err == nil) != tt.wantRun {
				t.Fatalf("marker error=%v, want run=%v", err, tt.wantRun)
			}
		})
	}
}
