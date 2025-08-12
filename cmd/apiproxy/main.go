package main

import (
	"context"
	"flag"
	"fmt"
	"lmtools/internal/logger"
	"lmtools/internal/proxy"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options]

apiproxy is an HTTP proxy server that provides an Anthropic-compatible API interface 
for various AI model providers.

Server Options:
  -host string               Host to bind the server to (default: "127.0.0.1")
  -port int                  Port to bind the server to (default: 8082)

Provider Options:
  -provider string           Provider: argo, openai, google, anthropic (default: "argo")
  -provider-url string       Custom URL for the selected provider (overrides default)
  -api-key-file string       Path to file containing API key for the selected provider
                            (required for non-Argo providers when not using custom URL)
  -argo-user string          Argo user (required for Argo provider)
  -argo-env string           Argo environment: prod, dev, or custom URL (default: "dev")

Model Options:
  -model string              Model to use (default varies by provider)
  -small-model string        Small model to use (default: "claudesonnet4")

Request Options:
  -max-request-body-size int Maximum request body size in MB (default: 10)

Logging Options:
  -log-level string          Log level: DEBUG, INFO, WARN, ERROR (default: "INFO")
  -log-format string         Log format: text, json (default: "text")
  -no-color                  Disable colored output

Examples:
  # Start proxy with Argo provider
  %s -argo-user myuser

  # Start proxy with OpenAI provider
  %s -provider openai -api-key-file ~/.openai-key

  # Start proxy with Gemini provider
  %s -provider google -api-key-file ~/.gemini-key

  # Start proxy with custom provider URL (no API key required)
  %s -provider openai -provider-url http://localhost:11434/v1

  # Start proxy on custom port with debug logging
  %s -port 8080 -log-level DEBUG -argo-user myuser
`,
		os.Args[0],
		os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

func main() {
	// Set custom usage function
	flag.Usage = printUsage
	// Parse command-line flags
	var (
		host string
		port int

		// API Key File (unified)
		apiKeyFile string

		// Configuration
		argoUser           string
		argoEnv            string
		preferredProvider  string
		providerURL        string
		model              string
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

	// API Key file flag (unified)
	flag.StringVar(&apiKeyFile, "api-key-file", "", "Path to file containing API key for the selected provider")

	// Configuration flags
	flag.StringVar(&argoUser, "argo-user", "", "Argo user")
	flag.StringVar(&argoEnv, "argo-env", "dev", "Argo environment")
	flag.StringVar(&preferredProvider, "provider", "argo", "Provider (openai, google, argo)")
	flag.StringVar(&providerURL, "provider-url", "", "Custom URL for the selected provider (overrides default)")
	flag.StringVar(&model, "model", "", "Model to use (default varies by provider)")
	flag.StringVar(&smallModel, "small-model", "claudesonnet4", "Small model to use")
	flag.Int64Var(&maxRequestBodySize, "max-request-body-size", 10, "Maximum request body size in MB")

	// Logging flags
	flag.StringVar(&logLevel, "log-level", "INFO", "Log level (DEBUG, INFO, WARN, ERROR)")
	flag.StringVar(&logFormat, "log-format", "text", "Log format (text, json)")
	flag.BoolVar(&noColor, "no-color", false, "Disable colored output")

	flag.Parse()

	// Read API key from file based on provider
	var anthropicAPIKey, openAIAPIKey, geminiAPIKey string

	if apiKeyFile != "" {
		apiKey, err := readKeyFile(apiKeyFile)
		if err != nil {
			log.Fatalf("Failed to read API key file: %v", err)
		}

		// Assign the API key to the appropriate provider
		switch preferredProvider {
		case "openai":
			openAIAPIKey = apiKey
		case "google":
			geminiAPIKey = apiKey
		case "anthropic":
			anthropicAPIKey = apiKey
		case "argo":
			// Argo doesn't use API keys, ignore
		default:
			log.Fatalf("Unknown provider: %s", preferredProvider)
		}
	} else if preferredProvider != "argo" && providerURL == "" {
		// For non-Argo providers without custom URL, API key is required
		log.Fatalf("API key file is required for %s provider. Use -api-key-file flag.", preferredProvider)
	}

	// Create configuration from flags
	config := &proxy.Config{
		AnthropicAPIKey:    anthropicAPIKey,
		OpenAIAPIKey:       openAIAPIKey,
		GeminiAPIKey:       geminiAPIKey,
		ArgoUser:           argoUser,
		ArgoEnv:            argoEnv,
		Provider:           preferredProvider,
		ProviderURL:        providerURL,
		Model:              model,
		SmallModel:         smallModel,
		MaxRequestBodySize: maxRequestBodySize * 1024 * 1024, // Convert MB to bytes
	}

	// Apply dynamic model defaults based on provider
	config.ApplyDynamicModelDefaults()

	// Initialize URLs
	config.InitializeURLs()

	// Validate configuration
	if err := config.Validate(); err != nil {
		log.Fatalf("Failed to validate configuration: %v", err)
	}

	// Configure logging
	if err := logger.Initialize("", logLevel, logFormat, noColor); err != nil {
		log.Printf("Failed to initialize logger: %v", err)
	}

	// Create and configure server
	server := proxy.NewServer(config)

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
		logger.Infof("Starting API proxy server on %s:%d", host, port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Failed to start server: %v", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	logger.Infof("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
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
