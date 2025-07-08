package argo

import (
	"fmt"
	"log"
	"os"
	"strings"
)

type logLevel int

const (
	_ logLevel = iota
	InfoLevel
	DebugLevel
)

const DefaultLogLevel = "info"

var currentLogLevel = InfoLevel

func InitLogging(level string) error {
	lvl := strings.ToLower(level)
	flags := log.LstdFlags
	switch lvl {
	case DefaultLogLevel:
		currentLogLevel = InfoLevel
	case "debug":
		currentLogLevel = DebugLevel
		flags |= log.Lshortfile
	default:
		return fmt.Errorf("invalid log level %q", level)
	}
	log.SetFlags(flags)
	log.SetOutput(os.Stderr)
	return nil
}

func Infof(format string, args ...interface{}) {
	if currentLogLevel >= InfoLevel {
		log.Printf("[INFO] "+format, args...)
	}
}

func Debugf(format string, args ...interface{}) {
	if currentLogLevel >= DebugLevel {
		log.Printf("[DEBUG] "+format, args...)
	}
}
