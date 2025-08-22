package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DirPerm is the standard directory permission for log directories
const DirPerm = 0o750

var (
	// Global logger instance
	globalLogger *Logger
	once         sync.Once

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

// OutputMode for backward compatibility (deprecated)
type OutputMode int

const (
	OutputAuto OutputMode = iota
	OutputStderrOnly
	OutputFileOnly
	OutputBoth
)

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
	counter int64 // atomic

	// Error tracking
	writeErrors int64 // atomic counter for write failures

	// Formatting options
	useJSON bool
}

// InitializeWithOptions sets up the global logger with options
func InitializeWithOptions(opts ...Option) error {
	var err error
	once.Do(func() {
		cfg := defaultConfig()
		for _, o := range opts {
			o(&cfg)
		}
		applyAutoDefaults(&cfg)
		globalLogger = newLogger(cfg)
		err = globalLogger.initSinks(cfg)
		globalLogger.initialized = true
	})
	return err
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

// WithOutputMode for backward compatibility (deprecated)
func WithOutputMode(m OutputMode) Option {
	return func(c *Config) {
		switch m {
		case OutputStderrOnly:
			c.ToStderr = true
			c.ToFile = false
			c.StderrMinLevel = c.Level // Use configured level for stderr
		case OutputFileOnly:
			c.ToStderr = false
			c.ToFile = true
		case OutputBoth:
			c.ToStderr = true
			c.ToFile = true
		default: // OutputAuto
			// Will be handled by applyAutoDefaults
		}
	}
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

// GetLogger returns the global logger instance
func GetLogger() *Logger {
	if globalLogger == nil {
		// Create a default logger if not initialized
		_ = InitializeWithOptions(
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
	if globalLogger != nil {
		globalLogger.Close()
	}
	globalLogger = nil
	once = sync.Once{}
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
		if err := os.MkdirAll(cfg.LogDir, DirPerm); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
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
	
	file, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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

// logf is the core logging function
func (l *Logger) logf(level int, format string, args ...interface{}) {
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
	if err := os.MkdirAll(dir, DirPerm); err != nil {
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

// ctxKey is the context key for storing ScopedLogger
type ctxKey struct{}

// WithContext stores a ScopedLogger in the context
func WithContext(ctx context.Context, sc *ScopedLogger) context.Context {
	return context.WithValue(ctx, ctxKey{}, sc)
}

// From retrieves ScopedLogger from context, or returns a default
func From(ctx context.Context) *ScopedLogger {
	if sc, ok := ctx.Value(ctxKey{}).(*ScopedLogger); ok && sc != nil {
		return sc
	}
	return GetLogger().NewScope("")
}

// ScopedLogger provides request-scoped logging
type ScopedLogger struct {
	parent *Logger
	id     int64
	start  time.Time
}

// NewScope creates a new scoped logger
func (l *Logger) NewScope(name string) *ScopedLogger {
	id := atomic.AddInt64(&l.counter, 1)
	sc := &ScopedLogger{parent: l, id: id, start: time.Now()}
	return sc
}

// Done logs the completion with duration
func (sc *ScopedLogger) Done() {
	if sc.id > 0 {
		dur := time.Since(sc.start)
		sc.Infof("done in %v", dur)
	}
}

// GetRequestID returns the request ID
func (sc *ScopedLogger) GetRequestID() int64 {
	return sc.id
}

// GetDuration returns the time elapsed since the scope started
func (sc *ScopedLogger) GetDuration() time.Duration {
	return time.Since(sc.start)
}

// GetStartTime returns the time when the scope started
func (sc *ScopedLogger) GetStartTime() time.Time {
	return sc.start
}

// Logging methods for ScopedLogger
func (sc *ScopedLogger) Debugf(format string, args ...interface{}) {
	sc.logf(LevelDebug, format, args...)
}

func (sc *ScopedLogger) Infof(format string, args ...interface{}) {
	sc.logf(LevelInfo, format, args...)
}

func (sc *ScopedLogger) Warnf(format string, args ...interface{}) {
	sc.logf(LevelWarn, format, args...)
}

func (sc *ScopedLogger) Errorf(format string, args ...interface{}) {
	sc.logf(LevelError, format, args...)
}

// IsDebugEnabled returns true if debug logging is enabled
func (sc *ScopedLogger) IsDebugEnabled() bool {
	if sc == nil || sc.parent == nil {
		return false
	}
	return sc.parent.IsDebugEnabled()
}

// IsInfoEnabled returns true if info logging is enabled
func (sc *ScopedLogger) IsInfoEnabled() bool {
	if sc == nil || sc.parent == nil {
		return false
	}
	return sc.parent.IsInfoEnabled()
}

// JSON helper methods

// DebugJSON logs JSON data at debug level
func (sc *ScopedLogger) DebugJSON(label string, v interface{}) {
	if !sc.IsDebugEnabled() {
		return
	}
	if b, err := json.Marshal(v); err == nil {
		sc.Debugf("%s: %s", label, string(b))
	} else {
		sc.Debugf("%s: <marshal error: %v>", label, err)
	}
}

// InfoJSON logs JSON at info level (typically pre-truncated by caller)
func (sc *ScopedLogger) InfoJSON(label string, v interface{}) {
	if !sc.IsInfoEnabled() {
		return
	}
	if b, err := json.Marshal(v); err == nil {
		sc.Infof("%s: %s", label, string(b))
	} else {
		sc.Infof("%s: <marshal error>", label)
	}
}

// logf is the core logging function for scoped logger
func (sc *ScopedLogger) logf(level int, format string, args ...interface{}) {
	if sc.parent == nil || level < sc.parent.level {
		return
	}

	now := time.Now().UTC()
	message := fmt.Sprintf(format, args...)

	// Format the message once
	var buf []byte
	if sc.parent.useJSON {
		// JSON format
		m := map[string]interface{}{
			"time":       now.Format(time.RFC3339Nano),
			"level":      levelNames[level],
			"message":    message,
			"request_id": sc.id,
		}
		if sc.parent.component != "" {
			m["component"] = sc.parent.component
		}
		buf, _ = json.Marshal(m)
		buf = append(buf, '\n')
	} else {
		// Text format
		b := &bytes.Buffer{}
		fmt.Fprintf(b, "[%s] [%s]", levelNames[level], now.Format(time.RFC3339Nano))
		if sc.parent.component != "" {
			fmt.Fprintf(b, " [%s]", sc.parent.component)
		}
		if sc.id > 0 {
			fmt.Fprintf(b, " [#%d]", sc.id)
		}
		fmt.Fprintf(b, " %s\n", message)
		buf = b.Bytes()
	}

	sc.parent.mu.Lock()
	defer sc.parent.mu.Unlock()

	// Write to stderr if enabled and level meets threshold
	if sc.parent.toStderr && level >= sc.parent.stderrMinLevel {
		if _, err := os.Stderr.Write(buf); err != nil {
			atomic.AddInt64(&sc.parent.writeErrors, 1)
		}
	}

	// Write to file if enabled and level meets threshold
	if sc.parent.toFile && level >= sc.parent.fileMinLevel {
		// Ensure file is open (lazy initialization) - already holding mutex
		if err := sc.parent.ensureFileOpenLocked(); err != nil {
			atomic.AddInt64(&sc.parent.writeErrors, 1)
		} else if sc.parent.logFile != nil {
			if _, err := sc.parent.logFile.Write(buf); err != nil {
				atomic.AddInt64(&sc.parent.writeErrors, 1)
			}
		}
	}
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

func ResetRequestCounter() {
	if globalLogger != nil {
		atomic.StoreInt64(&globalLogger.counter, 0)
	}
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
