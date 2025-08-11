package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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

	cfg, err := config.ParseFlags(os.Args[1:])
	if err != nil {
		return fmt.Errorf("invalid flags: %w", err)
	}

	// Initialize logging
	logDir := logger.GetLogDir()
	if err := logger.InitializeSimple(logDir); err != nil {
		// Log error to stderr but continue - logging is not critical
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize logging: %v\n", err)
	}
	defer logger.Close()

	// Set custom sessions directory if provided
	if cfg.SessionsDir != "" {
		// Validate and convert to absolute path if needed
		absDir, err := filepath.Abs(cfg.SessionsDir)
		if err != nil {
			return fmt.Errorf("invalid sessions directory: %w", err)
		}

		// Create directory if it doesn't exist
		if err := os.MkdirAll(absDir, 0o750); err != nil {
			return fmt.Errorf("failed to create sessions directory: %w", err)
		}

		// Verify it's a directory (not a file)
		info, err := os.Stat(absDir)
		if err != nil {
			return fmt.Errorf("failed to access sessions directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("sessions-dir path exists but is not a directory: %s", absDir)
		}

		session.SetSessionsDir(absDir)
		logger.Infof("Using custom sessions directory: %s", absDir)
	}

	// Set skip flock check if provided
	if cfg.SkipFlockCheck {
		session.SetSkipFlockCheck(true)
	}

	// Handle show-sessions flag
	if cfg.ShowSessions {
		return session.ShowSessions()
	}

	// Handle show flag
	if cfg.Show != "" {
		return session.ShowDispatcher(cfg.Show)
	}

	// Handle delete flag
	if cfg.Delete != "" {
		return session.DeleteNode(cfg.Delete)
	}

	// Check if we're branching from an assistant message (regeneration)
	isRegeneration := false
	if cfg.Branch != "" {
		isAssistant, err := session.IsAssistantMessage(cfg.Branch)
		if err != nil {
			return fmt.Errorf("failed to check branch message type: %w", err)
		}
		isRegeneration = isAssistant
	}

	// Only read stdin if not regenerating
	var inputStr string
	if !isRegeneration {
		inputBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read from STDIN: %w", err)
		}

		// Combined validation
		if len(inputBytes) > core.MaxInputSizeBytes {
			return fmt.Errorf("input too large: %d bytes (max: %d bytes)", len(inputBytes), core.MaxInputSizeBytes)
		}

		inputStr = strings.TrimSpace(string(inputBytes))

		// Only require input if not regenerating
		if inputStr == "" {
			return fmt.Errorf("input cannot be empty")
		}
	}

	// Handle sessions
	var sess *session.Session
	var sessionErr error

	if !cfg.NoSession {
		sess, sessionErr = handleSession(&cfg, inputStr, isRegeneration)
		if sessionErr != nil {
			return fmt.Errorf("session error: %w", sessionErr)
		}
	}

	var req *http.Request
	var body []byte

	if sess != nil {
		if isRegeneration {
			// For regeneration, build request with the created session
			req, body, err = core.BuildRegenerationRequest(cfg, sess, session.GetLineageAdapter)
		} else {
			req, body, err = core.BuildRequestWithSession(cfg, sess, session.GetLineageAdapter)
		}
	} else {
		req, body, err = core.BuildRequest(cfg, inputStr)
	}

	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	// Log request
	opName := getOperationName(&cfg)
	if err := logger.LogJSON(logger.GetLogDir(), opName, body); err != nil {
		return fmt.Errorf("failed to log request: %w", err)
	}

	// Create pooled HTTP client with retry logic
	retryClient := retry.NewClientWithRetries(cfg.Timeout, cfg.Retries, logger.GetLogger())

	// Send request with retry using pooled client
	resp, err := retryClient.Do(ctx, req, "lmc")
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

	out, err := core.HandleResponse(ctx, cfg, resp, logger.DefaultLogger())
	if err != nil {
		return fmt.Errorf("failed to handle response: %w", err)
	}

	// Save response to session if enabled
	if sess != nil && out != "" {
		responseMsg := session.Message{
			Role:      "assistant",
			Content:   strings.TrimSpace(out),
			Timestamp: time.Now(),
			Model:     getActualModel(cfg),
		}
		path, msgID, err := session.AppendMessage(sess, responseMsg)
		if err != nil {
			// Log error but don't fail the request
			fmt.Fprintf(os.Stderr, "Warning: failed to save response to session: %v\n", err)
		} else if path != sess.Path {
			// Update session path if a sibling was created
			sess.Path = path
			fmt.Fprintf(os.Stderr, "Note: Response saved to sibling branch %s as message %s\n",
				session.GetSessionID(path), msgID)
		}
	}

	// Explicit cancel before returning (good practice)
	cancel()

	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func handleSession(cfg *config.Config, inputStr string, isRegeneration bool) (*session.Session, error) {
	var sess *session.Session
	var err error

	// Determine how to handle the session
	if cfg.Resume != "" {
		// Resume or branch based on the provided ID
		sess, err = handleResumeOrBranch(cfg.Resume, inputStr)
	} else if cfg.Branch != "" {
		// Explicit branch
		sessionPath, messageID := session.ParseMessageID(cfg.Branch)
		siblingPath, err := session.CreateSibling(sessionPath, messageID)
		if err != nil {
			return nil, fmt.Errorf("failed to create branch: %w", err)
		}
		sess, err = session.LoadSession(siblingPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load branch session: %w", err)
		}
	} else {
		// Create new session
		sess, err = session.CreateSession()
	}

	if err != nil {
		return nil, err
	}

	// Only save user input if not regenerating
	if !isRegeneration && inputStr != "" {
		userMsg := session.Message{
			Role:      "user",
			Content:   inputStr,
			Timestamp: time.Now(),
		}

		path, msgID, err := session.AppendMessage(sess, userMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to save user message: %w", err)
		}

		// Update session path if a sibling was created
		if path != sess.Path {
			sess.Path = path
			// Log that we're using a sibling
			fmt.Fprintf(os.Stderr, "Note: Using sibling branch %s for message %s\n",
				session.GetSessionID(path), msgID)
		}
	}

	return sess, nil
}

func handleResumeOrBranch(resumeID string, inputStr string) (*session.Session, error) {
	// For -resume, we only accept session IDs (directories), not message IDs

	// First try as absolute path
	if info, err := os.Stat(resumeID); err == nil && info.IsDir() {
		// It's an absolute session path
		return session.LoadSession(resumeID)
	}

	// Then try as relative to sessions dir
	sessionPath := filepath.Join(session.GetSessionsDir(), resumeID)
	if info, err := os.Stat(sessionPath); err == nil && info.IsDir() {
		// It's a session ID - continue in that session
		return session.LoadSession(resumeID)
	}

	// Check if it looks like a message ID (contains a hex component after /)
	if strings.Contains(resumeID, "/") {
		parts := strings.Split(resumeID, "/")
		lastPart := parts[len(parts)-1]
		if _, err := strconv.ParseUint(lastPart, 16, 64); err == nil {
			// It's a message ID - this is not allowed for -resume
			return nil, fmt.Errorf("-resume requires a session ID (directory path), not a message ID; use -branch to create a branch from a message")
		}
	}

	// Not found as directory
	return nil, fmt.Errorf("session not found: %s", resumeID)
}

func getOperationName(cfg *config.Config) string {
	if cfg.Embed {
		return "embed_input"
	}
	if cfg.StreamChat {
		return "stream_chat_input"
	}
	return "chat_input"
}

// buildRegenerationRequest builds a request for regenerating an assistant message
// getActualModel returns the actual model that will be used for the request
func getActualModel(cfg config.Config) string {
	if cfg.Model != "" {
		return cfg.Model
	}
	if cfg.Embed {
		return core.DefaultEmbedModel
	}
	return core.DefaultChatModel
}

// getExitCode returns the appropriate exit code for an error
func getExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}

	// Check for interruption
	if errors.Is(err, core.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return exitInterrupted
	}

	// Everything else is a general error
	return exitError
}
