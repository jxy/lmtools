package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"os"
	"strings"
)

// cliApprover implements the core.Approver interface for CLI interaction
type cliApprover struct {
	notifier core.Notifier
}

// NewCliApprover creates a new CLI approver with a notifier
func NewCliApprover(notifier core.Notifier) *cliApprover {
	return &cliApprover{notifier: notifier}
}

func (a *cliApprover) Approve(ctx context.Context, command []string) (bool, error) {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	// Interactive prompt via stderr/stdin
	cmdJSON, _ := json.Marshal(command)
	a.notifier.Infof("\n>>> Tool wants to execute command: %s", string(cmdJSON))
	a.notifier.Promptf(">>> Allow execution? (y/N): ")

	// Create a channel to receive the user's response
	responseChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Read response in a goroutine so we can handle context cancellation
	// Note: This goroutine will terminate when stdin reaches EOF or when the process exits.
	// We cannot cleanly cancel a blocking read on stdin, but the goroutine will not leak
	// as it will exit when the process terminates or stdin is closed.
	//
	// EOF handling: io.EOF is treated as "no" (deny) to ensure non-interactive contexts
	// default to safe denial. This prevents accidental command execution when stdin is closed.
	go func() {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			// Notify user that EOF was encountered and denial is automatic
			a.notifier.Infof("\n>>> No interactive input available; denying by default")
			select {
			case responseChan <- "":
			case <-ctx.Done():
			}
			return
		} else if err != nil {
			select {
			case errChan <- errors.WrapError("read response", err):
			case <-ctx.Done():
				// Context cancelled, don't send error
			}
			return
		}
		select {
		case responseChan <- strings.TrimSpace(strings.ToLower(line)):
		case <-ctx.Done():
			// Context cancelled, don't send response
		}
	}()

	// Wait for either response or context cancellation
	select {
	case <-ctx.Done():
		// Context cancelled, return immediately
		a.notifier.Infof("\n>>> Approval prompt cancelled")
		return false, ctx.Err()
	case err := <-errChan:
		return false, err
	case response := <-responseChan:
		// Valid positive responses: "y" or "yes" (case-insensitive, trimmed)
		// All other inputs (including empty string from EOF) result in denial
		if response == "y" || response == "yes" {
			return true, nil
		}
		return false, nil
	}
}
