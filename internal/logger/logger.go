// Package logger provides structured logging for lmtools.
//
// Usage conventions:
// - In cmd/... and internal/core/...: Use logger.GetLogger() for the singleton instance
// - In internal/proxy/...: Use logger.From(ctx) for request-scoped logging
// - Never use fmt.Printf/Println for logging - always use the logger
//
// The logger supports multiple outputs (file and stderr) with different log levels
// for each output. It also provides lazy initialization to avoid creating log files
// for read-only operations.
package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RequestCounterKey is the context key for counter-based request IDs
type RequestCounterKey struct{}

// RequestIDKey is the context key for X-Request-ID correlation
type RequestIDKey struct{}

var (
	// Global logger instance
	globalLogger *Logger
	getLoggerMu  sync.Mutex // Protects GetLogger initialization check

	// Log levels
	LevelUnset = -1 // Sentinel value for unset level
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

// Option is a functional option for configuring the logger
type Option func(*Config)

// Config holds logger configuration
type Config struct {
	LogDir         string
	Level          int
	Format         string // "text" or "json"
	ToStderr       bool   // Write to stderr
	ToFile         bool   // Write to file (requires LogDir)
	StderrMinLevel int
	FileMinLevel   int
	Component      string
}

// Logger handles logging with levels and formatting
type Logger struct {
	mu      sync.Mutex
	level   int
	logFile *os.File
	// logDir is the configured directory for process logs and is set during InitializeWithOptions.
	// JSON logs use the dir passed to LogJSON and do not implicitly fall back to this value.
	logDir      string
	component   string
	initialized bool

	// Output configuration
	toStderr       bool
	toFile         bool
	stderrMinLevel int
	fileMinLevel   int

	// Lazy file creation - prevents empty log files for read-only operations
	filePath string // path for lazy creation (empty string means disabled)

	// Request scoping
	requestID string // Set when created via From(ctx)

	// Error tracking
	writeErrors int64 // atomic counter for write failures

	// Formatting options
	useJSON bool
}

// RequestLogger is a lightweight wrapper around Logger that includes request-specific context
type RequestLogger struct {
	*Logger
	requestID string
}

// InitializeWithOptions sets up the global logger with options
func InitializeWithOptions(opts ...Option) error {
	getLoggerMu.Lock()
	defer getLoggerMu.Unlock()
	return initializeWithOptionsLocked(opts...)
}

// initializeWithOptionsLocked initializes the logger while holding getLoggerMu
// Caller MUST hold getLoggerMu before calling this function
func initializeWithOptionsLocked(opts ...Option) error {
	// Check if already initialized
	if globalLogger != nil && globalLogger.initialized {
		return nil
	}

	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	applyAutoDefaults(&cfg)
	globalLogger = newLogger(cfg)
	err := globalLogger.initSinks(cfg)
	if err != nil {
		return err
	}
	globalLogger.initialized = true
	return nil
}

// Option functions
func WithLogDir(dir string) Option {
	return func(c *Config) { c.LogDir = dir }
}

func WithLevel(level string) Option {
	return func(c *Config) { c.Level = parseLevel(level) }
}

func WithFormat(format string) Option {
	return func(c *Config) { c.Format = format }
}

func WithStderr(enabled bool) Option {
	return func(c *Config) { c.ToStderr = enabled }
}

func WithFile(enabled bool) Option {
	return func(c *Config) { c.ToFile = enabled }
}

func WithStderrMinLevel(level string) Option {
	return func(c *Config) { c.StderrMinLevel = parseLevel(level) }
}

func WithFileMinLevel(level string) Option {
	return func(c *Config) { c.FileMinLevel = parseLevel(level) }
}

func WithComponent(name string) Option {
	return func(c *Config) { c.Component = name }
}

// GetLogger returns the global logger instance.
// Use this for:
// - Server initialization and shutdown
// - Background tasks without request context
// - Test setup and utilities
// For request-scoped logging in HTTP handlers, use From(ctx) instead.
func GetLogger() *Logger {
	getLoggerMu.Lock()
	defer getLoggerMu.Unlock()

	if globalLogger == nil {
		// Create a default logger if not initialized
		// Use the locked version to avoid deadlock
		_ = initializeWithOptionsLocked(
			WithLevel("info"),
			WithFormat("text"),
			WithStderr(true),
			WithFile(false),
		)
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
	getLoggerMu.Lock()
	defer getLoggerMu.Unlock()

	if globalLogger != nil {
		globalLogger.Close()
	}
	globalLogger = nil
}

// defaultConfig returns default configuration
func defaultConfig() Config {
	return Config{
		Level:          LevelInfo,
		Format:         "text",
		ToStderr:       true,
		ToFile:         false,
		StderrMinLevel: LevelUnset, // Use sentinel for unset
		FileMinLevel:   LevelUnset, // Use sentinel for unset
	}
}

// applyAutoDefaults applies auto mode defaults based on logDir
func applyAutoDefaults(cfg *Config) {
	// Auto-detect based on LogDir if not explicitly set
	if cfg.LogDir != "" {
		cfg.ToFile = true
		// With log dir: file gets all, stderr gets info+
		// Only set defaults if not explicitly configured
		if cfg.FileMinLevel == LevelUnset {
			cfg.FileMinLevel = LevelDebug
		}
		if cfg.StderrMinLevel == LevelUnset {
			cfg.StderrMinLevel = LevelInfo
		}
	} else {
		// No log dir: stderr only, all levels match configured level
		cfg.ToFile = false
		// Only set if not explicitly configured
		if cfg.StderrMinLevel == LevelUnset {
			cfg.StderrMinLevel = cfg.Level
		}
	}
}

// newLogger creates a new logger instance
func newLogger(cfg Config) *Logger {
	return &Logger{
		level:     cfg.Level,
		logDir:    cfg.LogDir,
		component: cfg.Component,
		// Output configuration
		toStderr:       cfg.ToStderr,
		toFile:         cfg.ToFile,
		stderrMinLevel: cfg.StderrMinLevel,
		fileMinLevel:   cfg.FileMinLevel,
		// Formatting options
		useJSON: cfg.Format == "json",
	}
}

// initSinks initializes log outputs based on configuration
func (l *Logger) initSinks(cfg Config) error {
	// File output - prepare for lazy creation
	if cfg.ToFile && cfg.LogDir != "" {
		if err := os.MkdirAll(cfg.LogDir, constants.DirPerm); err != nil {
			return errors.WrapError("create log directory", err)
		}

		// Store the path for lazy creation (don't create file yet)
		timestamp := time.Now().Format("20060102T150405.000")
		component := cfg.Component
		if component == "" {
			component = "lmc"
		}
		filename := fmt.Sprintf("%s_%s_%d.log", timestamp, component, os.Getpid())
		l.filePath = filepath.Join(cfg.LogDir, filename)
		// Don't create the file yet - will be created on first write
	}

	return nil
}

// ensureFileOpenLocked creates the log file if not already created (lazy initialization).
// IMPORTANT: Caller must hold l.mu mutex.
func (l *Logger) ensureFileOpenLocked() error {
	if l.logFile != nil || l.filePath == "" {
		return nil // Already open or disabled
	}

	file, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, constants.FilePerm)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	l.logFile = file
	return nil
}

// Close closes the log file and permanently disables file logging.
// After Close, no new log file will be created even if logging is attempted.
func (l *Logger) Close() {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.logFile != nil {
		l.logFile.Close()
		l.logFile = nil
	}

	// Prevent any future file creation by clearing the path
	l.filePath = ""
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

// Context-aware log methods
func (l *Logger) DebugfCtx(ctx context.Context, format string, args ...interface{}) {
	requestID := extractRequestIDFromContext(ctx)
	l.logInternal(LevelDebug, requestID, format, args...)
}

func (l *Logger) InfofCtx(ctx context.Context, format string, args ...interface{}) {
	requestID := extractRequestIDFromContext(ctx)
	l.logInternal(LevelInfo, requestID, format, args...)
}

func (l *Logger) WarnfCtx(ctx context.Context, format string, args ...interface{}) {
	requestID := extractRequestIDFromContext(ctx)
	l.logInternal(LevelWarn, requestID, format, args...)
}

func (l *Logger) ErrorfCtx(ctx context.Context, format string, args ...interface{}) {
	requestID := extractRequestIDFromContext(ctx)
	l.logInternal(LevelError, requestID, format, args...)
}

// extractRequestIDFromContext extracts request ID from context
func extractRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	// Try to get counter-based request ID first
	if counterID := ctx.Value(RequestCounterKey{}); counterID != nil {
		if id, ok := counterID.(int64); ok && id > 0 {
			return fmt.Sprintf("%d", id)
		}
	}

	// Fall back to X-Request-ID if available
	if reqID := ctx.Value(RequestIDKey{}); reqID != nil {
		if id, ok := reqID.(string); ok && id != "" {
			return id
		}
	}

	return ""
}

// RequestLogger forwarding methods
func (r *RequestLogger) Debugf(format string, args ...interface{}) {
	r.logfWithRequestID(LevelDebug, r.requestID, format, args...)
}

func (r *RequestLogger) Infof(format string, args ...interface{}) {
	r.logfWithRequestID(LevelInfo, r.requestID, format, args...)
}

func (r *RequestLogger) Warnf(format string, args ...interface{}) {
	r.logfWithRequestID(LevelWarn, r.requestID, format, args...)
}

func (r *RequestLogger) Errorf(format string, args ...interface{}) {
	r.logfWithRequestID(LevelError, r.requestID, format, args...)
}

// From returns a logger with request ID from context
// This is the preferred method for getting a logger instance as it provides
// request-scoped logging with automatic request ID propagation.
// For contexts without a request ID, it returns a logger without request context.
func From(ctx context.Context) *RequestLogger {
	logger := GetLogger()
	if logger == nil || ctx == nil {
		return &RequestLogger{Logger: logger}
	}

	// Extract request ID using the helper function
	requestID := extractRequestIDFromContext(ctx)

	return &RequestLogger{
		Logger:    logger,
		requestID: requestID,
	}
}

// IsDebugEnabled returns true if debug logging is enabled
func (l *Logger) IsDebugEnabled() bool {
	if l == nil {
		return false
	}
	return (l.toStderr && LevelDebug >= l.stderrMinLevel) ||
		(l.toFile && LevelDebug >= l.fileMinLevel)
}

// IsInfoEnabled returns true if info logging is enabled
func (l *Logger) IsInfoEnabled() bool {
	if l == nil {
		return false
	}
	return (l.toStderr && LevelInfo >= l.stderrMinLevel) ||
		(l.toFile && LevelInfo >= l.fileMinLevel)
}

// logInternal is the unified internal logging implementation
func (l *Logger) logInternal(level int, requestID string, format string, args ...interface{}) {
	if l == nil || level < l.level {
		return
	}

	now := time.Now().UTC()
	message := fmt.Sprintf(format, args...)

	// Format the message once
	var buf []byte
	if l.useJSON {
		// JSON format
		m := map[string]interface{}{
			"time":    now.Format(time.RFC3339Nano),
			"level":   levelNames[level],
			"message": message,
		}
		if requestID != "" {
			m["request_id"] = requestID
		}
		if l.component != "" {
			m["component"] = l.component
		}
		buf, _ = json.Marshal(m)
		buf = append(buf, '\n')
	} else {
		// Text format
		b := &bytes.Buffer{}
		fmt.Fprintf(b, "[%s] [%s]", levelNames[level], now.Format(time.RFC3339Nano))
		if l.component != "" {
			fmt.Fprintf(b, " [%s]", l.component)
		}
		if requestID != "" {
			fmt.Fprintf(b, " [#%s]", requestID)
		}
		fmt.Fprintf(b, " %s\n", message)
		buf = b.Bytes()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Write to stderr if enabled and level meets threshold
	if l.toStderr && level >= l.stderrMinLevel {
		if _, err := os.Stderr.Write(buf); err != nil {
			atomic.AddInt64(&l.writeErrors, 1)
		}
	}

	// Write to file if enabled and level meets threshold
	if l.toFile && level >= l.fileMinLevel {
		// Ensure file is open (lazy initialization) - already holding mutex
		if err := l.ensureFileOpenLocked(); err != nil {
			atomic.AddInt64(&l.writeErrors, 1)
		} else if l.logFile != nil {
			if _, err := l.logFile.Write(buf); err != nil {
				atomic.AddInt64(&l.writeErrors, 1)
			}
		}
	}
}

// logf is the core logging function
func (l *Logger) logf(level int, format string, args ...interface{}) {
	l.logInternal(level, l.requestID, format, args...)
}

// logfWithRequestID is the core logging function with explicit request ID
func (l *Logger) logfWithRequestID(level int, requestID, format string, args ...interface{}) {
	l.logInternal(level, requestID, format, args...)
}

// GetLogDir returns the logger's configured log directory
func (l *Logger) GetLogDir() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logDir != "" {
		return l.logDir
	}
	return GetLogDir()
}

// CreateLogFile creates a log file with a timestamp
func (l *Logger) CreateLogFile(logDir, prefix string) (*os.File, string, error) {
	return createLogFile(logDir, prefix)
}

// LogJSON logs data as JSON
func (l *Logger) LogJSON(dir, operation string, data []byte) error {
	// Skip if no directory provided
	if dir == "" {
		if l.initialized {
			l.Debugf("LogJSON skipped: empty log directory")
		}
		return nil
	}

	if !l.initialized {
		return nil
	}

	// Ensure directory exists
	if err := os.MkdirAll(dir, constants.DirPerm); err != nil {
		return errors.WrapError("create log directory", err)
	}

	// Create filename with timestamp and process ID for uniqueness
	timestamp := time.Now().Format("20060102T150405.000")
	filename := fmt.Sprintf("%s_%s_%d.json", timestamp, sanitizeOp(operation), os.Getpid())
	logPath := filepath.Join(dir, filename)

	// Write data to file with secure permissions (0600)
	if err := os.WriteFile(logPath, data, constants.FilePerm); err != nil {
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

// GetLogDir returns the default log directory
func GetLogDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".lmc", "logs")
	}
	return filepath.Join(homeDir, ".lmc", "logs")
}

// GetWriteErrorCount returns the number of write errors encountered
func GetWriteErrorCount() int64 {
	if globalLogger == nil {
		return 0
	}
	return atomic.LoadInt64(&globalLogger.writeErrors)
}

// DebugJSON logs data as JSON only if debug logging is enabled.
// This helper reduces duplication and prevents expensive JSON marshaling
// when debug logging is disabled.
func DebugJSON(l *RequestLogger, label string, v any) {
	if l != nil && l.IsDebugEnabled() {
		if b, err := json.Marshal(v); err == nil {
			l.Debugf("%s: %s", label, string(b))
		}
	}
}

// Package-level request counter for generating unique request IDs
var requestCounter int64

// WithNewRequestCounter returns a context with a new request counter ID.
// This provides a unified way to generate request IDs across the codebase.
func WithNewRequestCounter(ctx context.Context) context.Context {
	counterID := atomic.AddInt64(&requestCounter, 1)
	return context.WithValue(ctx, RequestCounterKey{}, counterID)
}
