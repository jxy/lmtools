package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CreateLogFile creates a log file with a timestamp
func CreateLogFile(dir, operation string) (*os.File, string, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return nil, "", fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create filename with timestamp and PID
	timestamp := time.Now().Format("20060102T150405.000")
	filename := fmt.Sprintf("%s_%s_%d.log", timestamp, sanitizeOp(operation), os.Getpid())
	logPath := filepath.Join(dir, filename)

	// Create file with secure permissions (0600)
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create log file: %w", err)
	}

	return file, logPath, nil
}
