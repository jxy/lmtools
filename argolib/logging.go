// Package argo provides internal logging utilities.
// Console: Infof. File: LogJSON/CreateLogFile.
// Not intended for external callers.
package argo

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	maxOpLen = 30 // Windows MAX_PATH margin
)

// InitLogging initializes the logging configuration.
// This function should be called once at program startup.
func InitLogging(level string) error {
	// Ignore level parameter, always use info level
	log.SetFlags(log.LstdFlags)
	log.SetOutput(os.Stderr)
	return nil
}

func Infof(format string, args ...interface{}) {
	log.Printf("[INFO] "+format, args...)
}

func Warnf(format string, args ...interface{}) {
	log.Printf("[WARN] "+format, args...)
}

// LogLockOperation is deprecated and does nothing.
// Lock operations are simple enough that they don't need special logging.
func LogLockOperation(operation string, sessionPath string, fields map[string]interface{}) {
	// No-op: removed complex lock logging
}

// sanitizeOp ensures operation names are safe and reasonable length
func sanitizeOp(op string) string {
	// Basic sanitization: no path separators
	op = filepath.Base(op)
	// Convert non-alphanumeric characters to underscores
	op = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, op)
	// Limit length to avoid path issues on Windows
	if len(op) > maxOpLen {
		op = op[:maxOpLen]
	}
	return op
}

// LogJSON writes JSON data to a timestamped log file.
// Files are created with 0600 permissions (owner read/write only).
// Note: We don't sync to disk; the OS will flush on close.
func LogJSON(dir, operation string, data []byte) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	operation = sanitizeOp(operation)

	// os.CreateTemp uses 96 bits of randomness (more than our old 32-bit suffix)
	pattern := fmt.Sprintf("%s_%s_*.json",
		time.Now().Format("20060102T150405"), operation)

	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write log: %w", err)
	}

	// No explicit sync - for diagnostic logs, OS buffering is sufficient
	return nil
}

// CreateLogFile creates a timestamped log file for streaming output.
// Returns the file handle and path. Caller MUST close the file.
func CreateLogFile(dir, operation string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", fmt.Errorf("create log dir: %w", err)
	}

	operation = sanitizeOp(operation)

	pattern := fmt.Sprintf("%s_%s_*.log",
		time.Now().Format("20060102T150405"), operation)

	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", fmt.Errorf("create log file: %w", err)
	}

	return f, f.Name(), nil
}

// logBaseDir can be overridden for testing
var logBaseDir string

// GetLogDir returns the directory where log files should be stored.
// It follows the same pattern as GetSessionsDir(), placing logs under ~/.argo/logs
func GetLogDir() string {
	if logBaseDir != "" {
		return logBaseDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".argo", "logs")
	}
	return filepath.Join(homeDir, ".argo", "logs")
}
