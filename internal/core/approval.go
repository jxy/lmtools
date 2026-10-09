package core

import "strings"

// Approval carries the operator's decision and an optional denial reason.
// Its zero value denies permission. Reason has no effect on an approval.
type Approval struct {
	Approved bool
	Reason   string
}

// DenialReason attributes custom text to the operator while retaining the
// ordinary denial text when the operator supplied no reason.
func (a Approval) DenialReason() string {
	reason := strings.TrimSpace(a.Reason)
	if reason == "" {
		return "user denied permission"
	}
	return "user denied permission; reason provided by the user: " + reason
}
