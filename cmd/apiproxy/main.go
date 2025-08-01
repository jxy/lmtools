package main

import (
	"context"
	"flag"
	"fmt"
	"lmtools/internal/apiproxy"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	// Parse command-line flags
	var (
		host string
		port int

		// API Key Files
		anthropicAPIKeyFile string
		openAIAPIKeyFile    string
		geminiAPIKeyFile    string

		// Configuration
		argoUser           string
		argoEnv            string
		preferredProvider  string
		providerURL        string
		bigModel           string
		smallModel         string
		maxRequestBodySize int64

		// Logging
		logLevel  string
		logFormat string
		noColor   bool
	)

	// Server flags
	flag.StringVar(&host, "host", "127.0.0.1", "Host to bind the server to")
	flag.IntVar(&port, "port", 8082, "Port to bind the server to")

	// API Key file flags
	flag.StringVar(&anthropicAPIKeyFile, "anthropic-api-key-file", "", "Path to file containing Anthropic API key")
	flag.StringVar(&openAIAPIKeyFile, "openai-api-key-file", "", "Path to file containing OpenAI API key")
	flag.StringVar(&geminiAPIKeyFile, "gemini-api-key-file", "", "Path to file containing Gemini API key")

	// Configuration flags
	flag.StringVar(&argoUser, "argo-user", "", "Argo user")
	flag.StringVar(&argoEnv, "argo-env", "dev", "Argo environment")
	flag.StringVar(&preferredProvider, "preferred-provider", "argo", "Preferred provider (openai, google, argo)")
	flag.StringVar(&providerURL, "provider-url", "", "Custom URL for the selected provider (overrides default)")
	flag.StringVar(&bigModel, "big-model", "claudeopus4", "Big model to use")
	flag.StringVar(&smallModel, "small-model", "claudesonnet4", "Small model to use")
	flag.Int64Var(&maxRequestBodySize, "max-request-body-size", 10, "Maximum request body size in MB")

	// Logging flags
	flag.StringVar(&logLevel, "log-level", "INFO", "Log level (DEBUG, INFO, WARN, ERROR)")
	flag.StringVar(&logFormat, "log-format", "text", "Log format (text, json)")
	flag.BoolVar(&noColor, "no-color", false, "Disable colored output")

	flag.Parse()

	// Read API keys from files
	var anthropicAPIKey, openAIAPIKey, geminiAPIKey string
	var err error

	if anthropicAPIKeyFile != "" {
		anthropicAPIKey, err = readKeyFile(anthropicAPIKeyFile)
		if err != nil {
			log.Fatalf("Failed to read Anthropic API key file: %v", err)
		}
	}

	if openAIAPIKeyFile != "" {
		openAIAPIKey, err = readKeyFile(openAIAPIKeyFile)
		if err != nil {
			log.Fatalf("Failed to read OpenAI API key file: %v", err)
		}
	}

	if geminiAPIKeyFile != "" {
		geminiAPIKey, err = readKeyFile(geminiAPIKeyFile)
		if err != nil {
			log.Fatalf("Failed to read Gemini API key file: %v", err)
		}
	}

	// Create configuration from flags
	config := &apiproxy.Config{
		AnthropicAPIKey:    anthropicAPIKey,
		OpenAIAPIKey:       openAIAPIKey,
		GeminiAPIKey:       geminiAPIKey,
		ArgoUser:           argoUser,
		ArgoEnv:            argoEnv,
		PreferredProvider:  preferredProvider,
		ProviderURL:        providerURL,
		BigModel:           bigModel,
		SmallModel:         smallModel,
		MaxRequestBodySize: maxRequestBodySize * 1024 * 1024, // Convert MB to bytes
	}

	// Apply dynamic model defaults based on provider
	config.ApplyDynamicModelDefaults()

	// Initialize models lists
	config.InitializeModelLists()

	// Validate configuration
	if err := config.Validate(); err != nil {
		log.Fatalf("Failed to validate configuration: %v", err)
	}

	// Configure logging
	apiproxy.ConfigureLogging(logLevel, logFormat, noColor)

	// Create and configure server
	server := apiproxy.NewServer(config)

	// Create handler with proper middleware chain
	handler := server

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Handler:      handler,
		ReadTimeout:  15 * time.Minute,
		WriteTimeout: 15 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Starting API proxy server on %s:%d", host, port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

// readKeyFile reads an API key from a file, trimming whitespace
func readKeyFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", path, err)
	}

	// Trim whitespace and newlines
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("file %s is empty or contains only whitespace", path)
	}

	return key, nil
}
