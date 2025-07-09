package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	argo "lmtools/argolib"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Exit codes - moved from argolib/errors.go
const (
	exitSuccess      = 0
	exitGeneralError = 1
	exitUsageError   = 2
	exitNetworkError = 3
	exitAuthError    = 4
	exitTimeoutError = 5
	exitInterrupted  = 130 // Standard for SIGINT
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(getExitCode(err))
	}
}

func run() error {
	// Single context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := argo.ParseFlags(os.Args[1:])
	if err != nil {
		return fmt.Errorf("invalid flags: %w", err)
	}
	if err := argo.InitLogging(cfg.LogLevel); err != nil {
		return fmt.Errorf("invalid log-level: %w", err)
	}

	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read from STDIN: %w", err)
	}

	// Combined validation
	if len(inputBytes) > argo.MaxInputSizeBytes {
		return fmt.Errorf("input too large: %d bytes (max: %d bytes)", len(inputBytes), argo.MaxInputSizeBytes)
	}

	inputStr := strings.TrimSpace(string(inputBytes))
	if inputStr == "" {
		return fmt.Errorf("input cannot be empty")
	}

	req, body, err := argo.BuildRequest(cfg, inputStr)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	// Log request
	opName := getOperationName(&cfg)
	if err := argo.LogJSON(cfg.LogDir, opName, body); err != nil {
		return fmt.Errorf("failed to log request: %w", err)
	}

	client := argo.NewHTTPClient(cfg.Timeout)

	// Configure retry
	retryConfig := argo.RetryConfig{
		MaxAttempts:       cfg.Retries,
		InitialDelay:      cfg.BackoffTime,
		MaxDelay:          30 * time.Second,
		Multiplier:        2.0,
		JitterFactor:      0.1, // Small jitter to avoid thundering herd
		RespectRetryAfter: true,
		Timeout:           cfg.RequestTimeout,
	}

	// Send request with retry (direct synchronous call)
	resp, timeoutCancel, err := argo.SendRequestWithRetry(ctx, client, req, body, retryConfig)

	// Immediate cleanup of timeout cancel
	defer func() {
		if timeoutCancel != nil {
			timeoutCancel()
		}
	}()

	// Handle error with response cleanup
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Defer response cleanup for success case
	defer func() {
		if resp != nil && resp.Body != nil {
			if err := resp.Body.Close(); err != nil {
				argo.Debugf("failed to close response body: %v", err)
			}
		}
	}()

	out, err := argo.HandleResponse(ctx, cfg, resp)
	if err != nil {
		return fmt.Errorf("failed to handle response: %w", err)
	}

	// Explicit cancel before returning (good practice)
	cancel()

	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func getOperationName(cfg *argo.Config) string {
	if cfg.Embed {
		return "embed_input"
	}
	if cfg.StreamChat {
		return "stream_chat_input"
	}
	return "chat_input"
}

// getExitCode returns the appropriate exit code for an error
// Moved from argolib/errors.go
func getExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}

	// Check for specific error types
	var httpErr *argo.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			return exitAuthError
		case httpErr.StatusCode >= 500:
			return exitNetworkError
		}
	}

	// Check for retryable errors
	var retryErr *argo.RetryableError
	if errors.As(err, &retryErr) {
		switch {
		case retryErr.HTTPStatus == 401 || retryErr.HTTPStatus == 403:
			return exitAuthError
		case retryErr.HTTPStatus >= 500:
			return exitNetworkError
		}
	}

	// Check for interruption
	if errors.Is(err, argo.ErrInterrupted) {
		return exitInterrupted
	}

	// Check for context errors
	if errors.Is(err, context.Canceled) {
		return exitInterrupted
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exitTimeoutError
	}

	// Check for URL errors (connection refused, etc)
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Recursively check the wrapped error
		return getExitCode(urlErr.Err)
	}

	// Check for network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return exitTimeoutError
		}
		return exitNetworkError
	}

	// Check for usage errors
	errStr := err.Error()
	if strings.Contains(errStr, "invalid") ||
		strings.Contains(errStr, "required") ||
		strings.Contains(errStr, "flag") {
		return exitUsageError
	}

	return exitGeneralError
}
