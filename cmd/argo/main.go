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
	// Set up signal handling for graceful shutdown
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
	inputStr := strings.TrimSpace(string(inputBytes))

	// Validate input is not empty
	if inputStr == "" {
		return fmt.Errorf("input cannot be empty")
	}

	// Basic input sanitization - prevent extremely large inputs
	if len(inputBytes) > argo.MaxInputSizeBytes {
		return fmt.Errorf("input too large: %d bytes (max: %d bytes)", len(inputBytes), argo.MaxInputSizeBytes)
	}

	req, body, err := argo.BuildRequest(cfg, inputStr)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	opName := "embed_input"
	if !cfg.Embed {
		if cfg.StreamChat {
			opName = "stream_chat_input"
		} else {
			opName = "chat_input"
		}
	}
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
		JitterFactor:      0.0, // No jitter by default
		RespectRetryAfter: true,
		RequestTimeout:    cfg.RequestTimeout,
	}

	resp, err := argo.SendRequestWithRetry(ctx, client, req, body, retryConfig)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			argo.Debugf("failed to close response body: %v", err)
		}
	}()

	out, err := argo.HandleResponse(ctx, cfg, resp)
	if err != nil {
		return fmt.Errorf("failed to handle response: %w", err)
	}
	if out != "" {
		fmt.Print(out)
	}
	return nil
}
