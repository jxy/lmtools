package logger

import "os"

// LoggerAdapter wraps the global logger functions to implement core.Logger interface
type LoggerAdapter struct{}

func (l LoggerAdapter) GetLogDir() string {
	// If logger is initialized with a custom log directory, use that
	if globalLogger != nil && globalLogger.logDir != "" {
		return globalLogger.logDir
	}
	// Otherwise fall back to default
	return GetLogDir()
}

func (l LoggerAdapter) LogJSON(logDir, prefix string, data []byte) error {
	return LogJSON(logDir, prefix, data)
}

func (l LoggerAdapter) CreateLogFile(logDir, prefix string) (*os.File, string, error) {
	return CreateLogFile(logDir, prefix)
}

// DefaultLogger returns a logger adapter that uses the global logger functions
func DefaultLogger() LoggerAdapter {
	return LoggerAdapter{}
}
