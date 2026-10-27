package core

import (
	"context"
	"strings"
)

// Approval carries the operator's decision and an optional denial reason.
// Its zero value denies permission. Reason has no effect on an approval.
type Approval struct {
	Approved bool
	// Rest approves, along with this call, every later call in the round
	// that would ask. The operator answered with the whole round on screen.
	Rest   bool
	Reason string
}

const (
	userDenialText       = "user denied permission"
	userDenialReasonText = userDenialText + "; reason provided by the user: "
)

// DenialReason attributes custom text to the operator while retaining the
// ordinary denial text when the operator supplied no reason.
func (a Approval) DenialReason() string {
	reason := strings.TrimSpace(a.Reason)
	if reason == "" {
		return userDenialText
	}
	return userDenialReasonText + reason
}

// UserDenial reads a result reason DenialReason wrote: whether the operator
// denied the call, and the reason they gave, if any. The model reads the
// sentence as written; the operator, who wrote the reason, needs only it.
func UserDenial(reason string) (given string, denied bool) {
	if reason == userDenialText {
		return "", true
	}
	if given, ok := strings.CutPrefix(reason, userDenialReasonText); ok {
		return given, true
	}
	return "", false
}

// ApprovalTarget names the call a question is about when the question is
// asked apart from the call's review: in a round of several calls, every
// call is shown first and the questions follow.
type ApprovalTarget struct {
	Index, Total int
	// Remaining counts the later calls in the round that would ask too,
	// which an answer approving the rest of the round approves.
	Remaining int
}

type approvalTargetKey struct{}

// WithApprovalTarget names the call the approver is asked about.
func WithApprovalTarget(ctx context.Context, target ApprovalTarget) context.Context {
	return context.WithValue(ctx, approvalTargetKey{}, target)
}

// ApprovalTargetFrom returns the call an approver's question is about, when
// the executor asked apart from the call's review.
func ApprovalTargetFrom(ctx context.Context) (ApprovalTarget, bool) {
	target, ok := ctx.Value(approvalTargetKey{}).(ApprovalTarget)
	return target, ok
}
