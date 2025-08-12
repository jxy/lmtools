package logger

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// Global logger instance
	globalLogger *Logger
	once         sync.Once

	// Log levels
	LevelDebug = 0
	LevelInfo  = 1
	LevelWarn  = 2
	LevelError = 3

	levelNames = map[int]string{
		LevelDebug: "DEBUG",
		LevelInfo:  "INFO",
		LevelWarn:  "WARN",
		LevelError: "ERROR",
	}
)

// Logger handles logging with levels and formatting
type Logger struct {
	mu          sync.Mutex
	file        *os.File
	stdLogger   *log.Logger
	level       int
	useJSON     bool
	useColor    bool
	colors      Colors
	initialized bool
}

// Colors for terminal output
type Colors struct {
	Enabled bool
}

// Color functions
func (c Colors) Cyan(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[36m" + s + "\033[0m"
}

func (c Colors) Blue(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[34m" + s + "\033[0m"
}

func (c Colors) Green(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[32m" + s + "\033[0m"
}

func (c Colors) Yellow(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[33m" + s + "\033[0m"
}

func (c Colors) Red(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[31m" + s + "\033[0m"
}

func (c Colors) Magenta(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[35m" + s + "\033[0m"
}

func (c Colors) Bold(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[1m" + s + "\033[0m"
}

func (c Colors) Dim(s string) string {
	if !c.Enabled {
		return s
	}
	return "\033[2m" + s + "\033[0m"
}

// Initialize sets up the global logger
func Initialize(logDir, level, format string, noColor bool) error {
	var err error
	once.Do(func() {
		globalLogger = &Logger{
			level:    parseLevel(level),
			useJSON:  format == "json",
			useColor: !noColor && isTerminal(),
			colors:   Colors{Enabled: !noColor && isTerminal()},
		}

		if logDir != "" {
			err = globalLogger.initFileLogging(logDir)
		}

		globalLogger.initialized = true
	})
	return err
}

// InitializeSimple initializes with just a log directory (for backward compatibility)
func InitializeSimple(logDir string) error {
	return Initialize(logDir, "info", "text", false)
}

// GetLogger returns the global logger instance
func GetLogger() *Logger {
	if globalLogger == nil {
		// Create a default logger if not initialized
		_ = Initialize("", "info", "text", false)
	}
	return globalLogger
}

// Close closes the log file if open
func Close() {
	if globalLogger != nil {
		globalLogger.Close()
	}
}

// ResetForTesting resets the logger state for testing purposes
// This allows reinitialization with different settings
// WARNING: Only use this in tests!
func ResetForTesting() {
	if globalLogger != nil {
		globalLogger.Close()
	}
	globalLogger = nil
	once = sync.Once{}
}

// initFileLogging sets up file-based logging
func (l *Logger) initFileLogging(logDir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create log file with timestamp and PID
	timestamp := time.Now().Format("20060102T150405")
	filename := fmt.Sprintf("%s_lmc_%d.log", timestamp, os.Getpid())
	logPath := filepath.Join(logDir, filename)

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	l.file = file
	l.stdLogger = log.New(file, "", log.LstdFlags)

	return nil
}

// Close closes the log file
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Log methods
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.logf(LevelDebug, format, args...)
}

func (l *Logger) Infof(format string, args ...interface{}) {
	l.logf(LevelInfo, format, args...)
}

func (l *Logger) Warnf(format string, args ...interface{}) {
	l.logf(LevelWarn, format, args...)
}

func (l *Logger) Errorf(format string, args ...interface{}) {
	l.logf(LevelError, format, args...)
}

// logf is the core logging function
func (l *Logger) logf(level int, format string, args ...interface{}) {
	if l == nil || level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)

	// Write to file if configured
	if l.stdLogger != nil {
		if l.useJSON {
			entry := map[string]interface{}{
				"time":    timestamp,
				"level":   levelNames[level],
				"message": message,
			}
			if data, err := json.Marshal(entry); err == nil {
				l.stdLogger.Println(string(data))
			}
		} else {
			l.stdLogger.Printf("[%s] [%s] %s", levelNames[level], timestamp, message)
		}
	}

	// Write to stderr for visibility
	// For apiproxy (no log dir), always write to stderr including debug
	// For lmc (with log dir), only write non-debug to stderr
	if level > LevelDebug || l.stdLogger == nil {
		fmt.Fprintf(os.Stderr, "[%s] [%s] %s\n", levelNames[level], timestamp, message)
	}
}

// LogJSON logs data as JSON
func (l *Logger) LogJSON(dir, operation string, data []byte) error {
	if dir == "" || !l.initialized {
		return nil
	}

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create filename with timestamp and process ID for uniqueness
	timestamp := time.Now().Format("20060102T150405.000")
	filename := fmt.Sprintf("%s_%s_%d.json", timestamp, sanitizeOp(operation), os.Getpid())
	logPath := filepath.Join(dir, filename)

	// Write data to file with secure permissions (0600)
	if err := os.WriteFile(logPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write log file: %w", err)
	}

	return nil
}

// Helper functions
func parseLevel(level string) int {
	switch strings.ToLower(level) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func isTerminal() bool {
	fileInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

func sanitizeOp(op string) string {
	// Replace problematic characters with underscores
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
	)
	return replacer.Replace(op)
}

// Global convenience functions
func Debugf(format string, args ...interface{}) {
	GetLogger().Debugf(format, args...)
}

func Infof(format string, args ...interface{}) {
	GetLogger().Infof(format, args...)
}

func Warnf(format string, args ...interface{}) {
	GetLogger().Warnf(format, args...)
}

func Errorf(format string, args ...interface{}) {
	GetLogger().Errorf(format, args...)
}

func LogJSON(dir, operation string, data []byte) error {
	return GetLogger().LogJSON(dir, operation, data)
}

// GetLogDir returns the default log directory
func GetLogDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".lmc", "logs")
	}
	return filepath.Join(homeDir, ".lmc", "logs")
}
