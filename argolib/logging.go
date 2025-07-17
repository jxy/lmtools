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

var processLogFile *os.File

// InitLogging initializes the logging configuration.
// This function should be called once at program startup.
// It creates a per-process log file in ~/.argo/logs/
func InitLogging(level string) error {
	// Set up console logging to stderr
	log.SetFlags(0) // We'll add our own timestamps
	log.SetOutput(os.Stderr)

	// Create per-process log file
	logDir := GetLogDir()
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	// Create log file with PID
	filename := fmt.Sprintf("%s_argo_%d.log",
		time.Now().Format("20060102T150405"), os.Getpid())
	logPath := filepath.Join(logDir, filename)

	var err error
	processLogFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("create process log file: %w", err)
	}

	return nil
}

// CloseLogging closes the process log file
func CloseLogging() {
	if processLogFile != nil {
		processLogFile.Close()
	}
}

func logToFile(level, format string, args ...interface{}) {
	if processLogFile != nil {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(processLogFile, "%s [%s] %s\n", timestamp, level, msg)
	}
}

func Infof(format string, args ...interface{}) {
	// Log to file
	logToFile("INFO", format, args...)

	// Log to console
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("%s [INFO] "+format, append([]interface{}{timestamp}, args...)...)
}

func Warnf(format string, args ...interface{}) {
	// Log to file
	logToFile("WARN", format, args...)

	// Log to console
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("%s [WARN] "+format, append([]interface{}{timestamp}, args...)...)
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
