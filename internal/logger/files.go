package logger

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"time"
)

// createLogFile creates a log file with a timestamp (internal helper)
func createLogFile(dir, operation string) (*os.File, string, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dir, constants.DirPerm); err != nil {
		return nil, "", errors.WrapError("create log directory", err)
	}

	// Create filename with timestamp and PID
	timestamp := time.Now().Format("20060102T150405.000")
	filename := fmt.Sprintf("%s_%s_%d.log", timestamp, sanitizeOp(operation), os.Getpid())
	logPath := filepath.Join(dir, filename)

	// Create file with secure permissions (0600)
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, constants.FilePerm)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create log file: %w", err)
	}

	return file, logPath, nil
}
