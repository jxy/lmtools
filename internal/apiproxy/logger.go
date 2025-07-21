package apiproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// LogLevel represents the log level
type LogLevel int

const (
	LogLevelError LogLevel = iota
	LogLevelWarn
	LogLevelInfo
	LogLevelDebug
)

var (
	currentLogLevel LogLevel
	structuredMode  bool
	logMutex        sync.Mutex
)

func init() {
	// Set log level from environment
	switch os.Getenv("LOG_LEVEL") {
	case "DEBUG", "debug":
		currentLogLevel = LogLevelDebug
	case "INFO", "info":
		currentLogLevel = LogLevelInfo
	case "WARN", "warn":
		currentLogLevel = LogLevelWarn
	case "ERROR", "error":
		currentLogLevel = LogLevelError
	default:
		currentLogLevel = LogLevelInfo // Default to info
	}

	// Check if structured logging is enabled
	structuredMode = os.Getenv("LOG_FORMAT") == "json"
}

// ANSI color codes
type Colors struct {
	enabled bool
}

var Color Colors

func init() {
	// Check if we're in a TTY and colors are not disabled
	Color.enabled = os.Getenv("NO_COLOR") == "" && isTerminal()
}

func isTerminal() bool {
	// Simple check if stdout is a terminal
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func (c Colors) Cyan(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[96m" + s + "\033[0m"
}

func (c Colors) Blue(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[94m" + s + "\033[0m"
}

func (c Colors) Green(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[92m" + s + "\033[0m"
}

func (c Colors) Yellow(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[93m" + s + "\033[0m"
}

func (c Colors) Red(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[91m" + s + "\033[0m"
}

func (c Colors) Magenta(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[95m" + s + "\033[0m"
}

func (c Colors) Bold(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[1m" + s + "\033[0m"
}

func (c Colors) Dim(s string) string {
	if !c.enabled {
		return s
	}
	return "\033[2m" + s + "\033[0m"
}

// LogRequest logs a request in a beautiful format
func LogRequest(method, path, originalModel, mappedModel string, numMessages, numTools int, statusCode int) {
	LogRequestWithStream(method, path, originalModel, mappedModel, numMessages, numTools, statusCode, false)
}

// LogRequestWithStream logs a request with streaming indicator
func LogRequestWithStream(method, path, originalModel, mappedModel string, numMessages, numTools int, statusCode int, isStreaming bool) {
	if currentLogLevel < LogLevelInfo {
		return
	}

	// Add timestamp for clarity
	timestamp := time.Now().Format("15:04:05")

	// Format the original model name
	originalDisplay := Color.Cyan(originalModel)

	// Extract endpoint name
	endpoint := path
	if idx := strings.Index(endpoint, "?"); idx != -1 {
		endpoint = endpoint[:idx]
	}

	// Extract provider and model name
	mappedDisplay := mappedModel
	provider := ""
	if idx := strings.Index(mappedModel, "/"); idx != -1 {
		provider = mappedModel[:idx]
		mappedDisplay = mappedModel[idx+1:]
	}

	// Format provider
	providerStr := ""
	switch provider {
	case "openai":
		providerStr = Color.Yellow("[OpenAI]")
	case "gemini":
		providerStr = Color.Magenta("[Gemini]")
	case "argo":
		providerStr = Color.Blue("[Argo]")
	default:
		if provider != "" {
			providerStr = fmt.Sprintf("[%s]", provider)
		}
	}

	// Format tools and messages
	toolsStr := ""
	if numTools > 0 {
		toolsStr = Color.Magenta(fmt.Sprintf(" • %d tools", numTools))
	}
	messagesStr := Color.Dim(fmt.Sprintf(" • %d msgs", numMessages))

	// Format status code
	var statusStr string
	if statusCode == 200 {
		statusStr = Color.Green("✓")
	} else {
		statusStr = Color.Red(fmt.Sprintf("✗ %d", statusCode))
	}

	// Add streaming indicator
	streamStr := ""
	if isStreaming {
		streamStr = Color.Yellow(" [STREAM]")
	}

	// Put it all together in a single line
	fmt.Printf("\n%s %s %s %s %s %s → %s%s%s%s\n",
		Color.Dim(timestamp),
		statusStr,
		Color.Bold(method),
		endpoint,
		providerStr,
		originalDisplay,
		Color.Green(mappedDisplay),
		messagesStr,
		toolsStr,
		streamStr,
	)
	os.Stdout.Sync()
}

// LogEntry represents a structured log entry
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"`
	Message   string                 `json:"message"`
	Context   string                 `json:"context,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// logStructured outputs a structured log entry
func logStructured(level string, message string, fields map[string]interface{}) {
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   message,
		Fields:    fields,
	}

	logMutex.Lock()
	defer logMutex.Unlock()

	if err := json.NewEncoder(os.Stderr).Encode(entry); err != nil {
		// Fallback to plain text
		fmt.Fprintf(os.Stderr, "%s [%s] %s\n", entry.Timestamp.Format(time.RFC3339), level, message)
	}
}

// LogError logs an error in a formatted way
func LogError(context string, err error) {
	if structuredMode {
		fields := map[string]interface{}{
			"context": context,
		}
		if err != nil {
			fields["error"] = err.Error()
		}
		logStructured("ERROR", context, fields)
	} else {
		fmt.Printf("%s %s: %v\n", Color.Red("ERROR"), Color.Bold(context), err)
		os.Stdout.Sync()
	}
}

// LogInfo logs informational messages
func LogInfo(message string) {
	if currentLogLevel < LogLevelInfo {
		return
	}
	fmt.Printf("%s %s\n", Color.Blue("INFO"), message)
	os.Stdout.Sync()
}

// LogDebug logs debug messages
func LogDebug(message string) {
	if currentLogLevel < LogLevelDebug {
		return
	}
	fmt.Printf("%s %s\n", Color.Dim("DEBUG"), Color.Dim(message))
	os.Stdout.Sync()
}

// LogSeparator logs a visual separator
func LogSeparator() {
	if currentLogLevel < LogLevelDebug {
		return
	}
	fmt.Printf("%s\n", Color.Dim("────────────────────────────────────────"))
	os.Stdout.Sync()
}

// LogStreaming logs streaming events
func LogStreaming(event string, details string) {
	if currentLogLevel < LogLevelDebug {
		return
	}
	fmt.Printf("%s %s: %s\n", Color.Yellow("STREAM"), Color.Bold(event), details)
	os.Stdout.Sync()
}

// LogJSON logs JSON data in a pretty format with truncated content
func LogJSON(label string, data interface{}) {
	if currentLogLevel < LogLevelDebug {
		return
	}

	// Convert to JSON for pretty printing
	jsonBytes, err := json.MarshalIndent(data, "  ", "  ")
	if err != nil {
		LogDebug(fmt.Sprintf("%s: [error marshaling JSON: %v]", label, err))
		return
	}

	// Parse and truncate content fields
	var obj interface{}
	if err := json.Unmarshal(jsonBytes, &obj); err == nil {
		truncateContent(obj)
		jsonBytes, _ = json.MarshalIndent(obj, "  ", "  ")
	}

	fmt.Printf("\n%s %s\n%s\n", Color.Bold("───"), Color.Cyan(label), string(jsonBytes))
	os.Stdout.Sync()
}

// truncateContent recursively truncates content fields in JSON objects
func truncateContent(obj interface{}) {
	switch v := obj.(type) {
	case map[string]interface{}:
		for key, val := range v {
			// Don't truncate content fields - show them in full
			switch key {
			case "content", "text":
				// Keep the full content without truncation
			case "description":
				// Still truncate descriptions as they might be very long
				if str, ok := val.(string); ok && len(str) > 50 {
					v[key] = str[:50] + "... [" + fmt.Sprintf("%d chars", len(str)) + "]"
				}
			}
			// Always recurse into nested objects
			truncateContent(val)
		}
	case []interface{}:
		for _, item := range v {
			truncateContent(item)
		}
	}
}
