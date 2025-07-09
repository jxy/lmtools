package main

import (
	"context"
	"fmt"
	"io"
	argo "lmtools/argolib"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(argo.GetExitCode(err))
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
