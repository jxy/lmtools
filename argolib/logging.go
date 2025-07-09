// Package argo provides internal logging utilities.
// Console: Infof/Debugf. File: LogJSON/CreateLogFile.
// Not intended for external callers.
package argo

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

type logLevel int32

const (
	InfoLevel logLevel = iota + 1
	DebugLevel
)

const (
	DefaultLogLevel = "info"
	maxOpLen        = 30 // Windows MAX_PATH margin
)

var currentLogLevel int32 = int32(InfoLevel)

// InitLogging initializes the logging level and configuration.
// This function should be called once at program startup.
func InitLogging(level string) error {
	lvl := strings.ToLower(level)
	flags := log.LstdFlags
	var newLevel logLevel
	switch lvl {
	case DefaultLogLevel:
		newLevel = InfoLevel
	case "debug":
		newLevel = DebugLevel
		flags |= log.Lshortfile
	default:
		return fmt.Errorf("invalid log level %q", level)
	}
	atomic.StoreInt32(&currentLogLevel, int32(newLevel))
	log.SetFlags(flags)
	log.SetOutput(os.Stderr)
	return nil
}

func Infof(format string, args ...interface{}) {
	if logLevel(atomic.LoadInt32(&currentLogLevel)) >= InfoLevel {
		log.Printf("[INFO] "+format, args...)
	}
}

func Debugf(format string, args ...interface{}) {
	if logLevel(atomic.LoadInt32(&currentLogLevel)) >= DebugLevel {
		log.Printf("[DEBUG] "+format, args...)
	}
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
