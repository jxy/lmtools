package main

import (
	"context"
	"flag"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/providerconfig"
	"lmtools/internal/providers"
	"lmtools/internal/proxy"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type repeatableStringFlag []string

func (f *repeatableStringFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *repeatableStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options]

apiproxy is an HTTP proxy server that provides an Anthropic-compatible API interface 
for Anthropic, OpenAI, Google, and Argo providers.

Server Options:
  -host string               Host to bind the server to (default: "127.0.0.1")
  -port int                  Port to bind the server to (default: 8082)
  -sessions-dir string       Stateful Responses API sessions directory
                            (default: ~/.apiproxy/sessions)

Provider Options:
  -provider string           Provider: argo, anthropic, openai, google (default: "argo")
  -provider-url string       Custom URL for the selected provider (overrides default)
  -api-key-file string       Path to file containing API key for the selected provider
                            (required for openai/google/anthropic unless using custom URL;
                             also accepted for argo)
  -argo-user string          Argo user/API key (optional if -api-key-file is provided)
  -argo-dev                  Use the Argo dev environment instead of prod
  -argo-test                 Use the Argo test environment instead of prod
  -argo-legacy               Use legacy Argo /api/v1/resource chat endpoints

Model Options:
  -model-map REGEX=MODEL     Map matching request models to a backend model
                             (repeatable, first match wins)

Request Options:
  -max-request-body-size int Maximum request body size in MB (default: 10)

Logging Options:
  -log-level string          Log level: DEBUG, INFO, WARN, ERROR (default: "INFO")
  -log-format string         Log format: text, json (default: "text")
  -no-color                  Disable colored output

Examples:
  # Start proxy with Argo provider
  %s -argo-user myuser

  # Start proxy with Anthropic provider
  %s -provider anthropic -api-key-file ~/.anthropic-key

  # Start proxy with OpenAI provider
  %s -provider openai -api-key-file ~/.openai-key

  # Start proxy with Google provider
  %s -provider google -api-key-file ~/.google-key

  # Start proxy with custom provider URL (no API key required)
  %s -provider openai -provider-url http://localhost:11434/v1

  # Start proxy on custom port with debug logging
  %s -port 8080 -log-level DEBUG -argo-user myuser
`,
		os.Args[0],
		os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

func main() {
	// Set custom usage function
	flag.Usage = printUsage
	// Parse command-line flags
	var (
		host string
		port int

		// Configuration
		providerOpts       providerconfig.Options
		modelMapSpecs      repeatableStringFlag
		maxRequestBodySize int64
		sessionsDir        string

		// Logging
		logLevel  string
		logFormat string
	)

	// Server flags
	flag.StringVar(&host, "host", "127.0.0.1", "Host to bind the server to")
	flag.IntVar(&port, "port", 8082, "Port to bind the server to")

	// Configuration flags
	providerconfig.RegisterFlags(flag.CommandLine, &providerOpts, providerconfig.Defaults{Provider: constants.ProviderArgo})
	flag.Var(&modelMapSpecs, "model-map", "Map matching request models to a backend model as REGEX=MODEL (repeatable, first match wins)")
	flag.Int64Var(&maxRequestBodySize, "max-request-body-size", 10, "Maximum request body size in MB")
	flag.StringVar(&sessionsDir, "sessions-dir", "", "Stateful Responses API sessions directory (default: ~/.apiproxy/sessions)")

	// Logging flags
	flag.StringVar(&logLevel, "log-level", "INFO", "Log level (DEBUG, INFO, WARN, ERROR)")
	flag.StringVar(&logFormat, "log-format", "text", "Log format (text, json)")

	flag.Parse()

	// Read API key from file based on provider
	if err := providerOpts.Normalize(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to validate configuration: %v\n", err)
		os.Exit(1)
	}
	providerKeys, err := loadProviderKeys(providerOpts.Provider, providerOpts.APIKeyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load API key file: %v\n", err)
		os.Exit(1)
	}
	if err := providerOpts.ValidateCredentials(providers.ValidationSurfaceProxy, providerKeys, false); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to validate configuration: %v\n", err)
		os.Exit(1)
	}

	modelMapRules := make([]proxy.ModelMapRule, 0, len(modelMapSpecs))
	for _, spec := range modelMapSpecs {
		rule, err := proxy.ParseModelMapSpec(spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid -model-map %q: %v\n", spec, err)
			os.Exit(1)
		}
		modelMapRules = append(modelMapRules, rule)
	}

	// Create configuration from flags
	config := &proxy.Config{
		ProviderKeySet:     providerKeys,
		ArgoUser:           providerOpts.ArgoUser,
		ArgoDev:            providerOpts.ArgoDev,
		ArgoTest:           providerOpts.ArgoTest,
		ArgoLegacy:         providerOpts.ArgoLegacy,
		ArgoEnv:            providerOpts.ArgoEnv,
		Provider:           providerOpts.Provider,
		ProviderURL:        providerOpts.ProviderURL,
		ModelMapRules:      modelMapRules,
		MaxRequestBodySize: maxRequestBodySize * 1024 * 1024, // Convert MB to bytes
		SessionsDir:        sessionsDir,
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to validate configuration: %v\n", err)
		os.Exit(1)
	}

	// Configure logging
	if err := logger.InitializeWithOptions(
		logger.WithLevel(logLevel),
		logger.WithFormat(logFormat),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("apiproxy"),
	); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
	}

	// Create and configure server
	server, err := proxy.NewServer(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

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
		// No request context available at startup
		logger.GetLogger().Infof("Starting API proxy server on %s:%d", host, port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// No request context available during startup error
			logger.GetLogger().Errorf("Failed to start server: %v", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	// No request context available during shutdown
	logger.GetLogger().Infof("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		// No request context available during shutdown
		logger.GetLogger().Errorf("Server forced to shutdown: %v", err)
	}

	// No request context available during shutdown
	logger.GetLogger().Infof("Server exited")
}

func loadProviderKeys(preferredProvider, apiKeyFile string) (auth.ProviderKeySet, error) {
	if apiKeyFile == "" {
		return auth.ProviderKeySet{}, nil
	}
	providerKey, err := auth.LoadProviderKeyFile(preferredProvider, apiKeyFile)
	if err != nil {
		return auth.ProviderKeySet{}, err
	}
	return providerKey.Set(), nil
}
