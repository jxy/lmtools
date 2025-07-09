package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	argo "lmtools/argolib"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Exit codes - simplified to 3
const (
	exitSuccess     = 0   // Success
	exitError       = 1   // General error
	exitInterrupted = 130 // Standard for SIGINT
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
	// Initialize logging with info level (hardcoded)
	if err := argo.InitLogging("info"); err != nil {
		return fmt.Errorf("failed to init logging: %w", err)
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

	// Create HTTP client with timeout
	client := &http.Client{Timeout: cfg.Timeout}

	// Send request with retry (direct synchronous call)
	// Hardcoded backoff time of 1 second
	resp, err := argo.Retry(ctx, client, req, body, cfg.Retries, 1*time.Second)
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
			_ = resp.Body.Close()
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
func getExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}

	// Check for interruption
	if errors.Is(err, argo.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return exitInterrupted
	}

	// Everything else is a general error
	return exitError
}
