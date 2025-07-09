// Package argo provides a client library for interacting with the Argo API.
// It supports both chat and embedding operations with various AI models.
package argo

import (
	"fmt"
	"strings"
)

// Default models
const (
	DefaultChatModel  = "gemini25pro"
	DefaultEmbedModel = "v3large"
)

// API endpoints
const (
	ProdURL = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
	DevURL  = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
)

// Input validation constants
const (
	MaxInputSizeBytes = 1024 * 1024 // 1MB
)

// Supported chat models
var ChatModels = []string{
	"gpt35",
	"gpt35large",
	"gpt4",
	"gpt4large",
	"gpt4turbo",
	"gpt4o",
	"gpt4olatest",
	"gpto1mini",
	"gpto3mini",
	"gpto1",
	"gpto3",
	"gpto4mini",
	"gpt41",
	"gpt41mini",
	"gpt41nano",
	"gemini25pro",
	"gemini25flash",
	"claudeopus4",
	"claudesonnet4",
	"claudesonnet37",
	"claudesonnet35v2",
}

// Supported embedding models
var EmbedModels = []string{
	"ada002",
	"v3large",
	"v3small",
}

// Supported log levels
var LogLevels = []string{
	"info",
	"debug",
}

// Supported environments
var Environments = []string{
	"prod",
	"dev",
}

// ValidateChatModel checks if the provided model is a valid chat model
func ValidateChatModel(model string) error {
	if model == "" {
		return nil // empty is ok, will use default
	}
	for _, m := range ChatModels {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("invalid chat model %q, available models: %s", model, strings.Join(ChatModels, ", "))
}

// ValidateEmbedModel checks if the provided model is a valid embedding model
func ValidateEmbedModel(model string) error {
	if model == "" {
		return nil // empty is ok, will use default
	}
	for _, m := range EmbedModels {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("invalid embed model %q, available models: %s", model, strings.Join(EmbedModels, ", "))
}

// ValidateLogLevel checks if the provided log level is valid
func ValidateLogLevel(level string) error {
	for _, l := range LogLevels {
		if l == level {
			return nil
		}
	}
	return fmt.Errorf("invalid log level %q, available levels: %s", level, strings.Join(LogLevels, ", "))
}

// IsValidEnvironment checks if the provided environment is valid
func IsValidEnvironment(env string) bool {
	// Check predefined environments
	for _, e := range Environments {
		if e == env {
			return true
		}
	}
	// Check if it's a custom URL
	return strings.HasPrefix(env, "http://") || strings.HasPrefix(env, "https://")
}

// GetBaseURL returns the base URL for the given environment
func GetBaseURL(env string) string {
	switch env {
	case "prod":
		return ProdURL
	case "", "dev":
		return DevURL
	default:
		// Custom URL
		return env
	}
}
