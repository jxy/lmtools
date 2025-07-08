package argo

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

var defaultLogDir = os.ExpandEnv("$HOME/tmp/log/argo")

func getDefaultUser() string {
	return "" // No default, always require -u flag
}

type Config struct {
	Model          string        // model to use
	Embed          bool          // whether to run in embed mode
	StreamChat     bool          // whether to use streaming chat mode
	PromptChat     bool          // whether to use 'prompt' instead of 'messages' for chat
	LogDir         string        // directory for log files
	User           string        // user identifier
	System         string        // system prompt for chat
	Env            string        // environment (prod|dev|custom base URL)
	Timeout        time.Duration // HTTP request timeout
	LogLevel       string        // log level (info|debug)
	Retries        int           // number of retry attempts
	BackoffTime    time.Duration // initial retry backoff time
	RequestTimeout time.Duration // Per-request timeout (enforced per request)
}

func ParseFlags(args []string) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	// Set custom usage function
	fs.Usage = func() {
		printUsage()
	}

	// Model Options
	fs.StringVar(&cfg.Model, "m", "", fmt.Sprintf("model to use (default: %q for chat, %q for embed)\n\t\tChat models: %s\n\t\tEmbed models: %s",
		DefaultChatModel, DefaultEmbedModel,
		strings.Join(ChatModels, ", "),
		strings.Join(EmbedModels, ", ")))
	fs.BoolVar(&cfg.Embed, "e", false, "enable embed mode instead of chat")

	// Chat Options
	fs.BoolVar(&cfg.StreamChat, "stream", false, "use streaming chat mode")
	fs.BoolVar(&cfg.PromptChat, "prompt-chat", false, "use 'prompt' field instead of 'messages' for chat")
	fs.StringVar(&cfg.System, "s", "You are a brilliant assistant.", "system prompt for chat mode")

	// Configuration
	fs.StringVar(&cfg.Env, "env", "dev", "environment: prod, dev, or custom base URL")
	fs.StringVar(&cfg.User, "u", getDefaultUser(), "user identifier")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

	// Logging
	fs.StringVar(&cfg.LogDir, "logDir", defaultLogDir, "directory for log files")
	fs.StringVar(&cfg.LogLevel, "log-level", DefaultLogLevel, fmt.Sprintf("log level: %s", strings.Join(LogLevels, ", ")))

	// Retry configuration
	fs.IntVar(&cfg.Retries, "retries", 3, "number of retry attempts for failed requests")
	fs.DurationVar(&cfg.BackoffTime, "backoff", 1*time.Second, "initial retry backoff time")

	// Request timeout - defaults to 0 which means use the main timeout
	fs.DurationVar(&cfg.RequestTimeout, "request-timeout", 0,
		"timeout for individual requests (defaults to --timeout value)")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	// Validate environment
	if !IsValidEnvironment(cfg.Env) {
		return cfg, fmt.Errorf("invalid env: %q, must be one of: %s, or a custom URL (http://... or https://...)",
			cfg.Env, strings.Join(Environments, ", "))
	}

	// Validate log level
	if err := ValidateLogLevel(cfg.LogLevel); err != nil {
		return cfg, err
	}

	// Check invalid flag combinations
	if cfg.Embed && (cfg.StreamChat || cfg.PromptChat) {
		return cfg, fmt.Errorf("invalid flag combination: embed mode cannot be used with stream or prompt-chat")
	}

	// Validate user is provided
	if cfg.User == "" {
		return cfg, fmt.Errorf("user identifier (-u) is required")
	}

	// If RequestTimeout is not set (0), default to the main Timeout
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = cfg.Timeout
	}

	return cfg, nil
}

// printUsage prints a custom usage message
func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] < input

Argo is a command-line interface for AI model interactions.

Model Options:
  -m string      Model to use (default: %q for chat, %q for embed)
                 Chat models: %s
                 Embed models: %s
  -e             Enable embed mode instead of chat (default: false)

Chat Options:
  -stream        Use streaming chat mode (default: false)
  -prompt-chat   Use 'prompt' field instead of 'messages' for chat (default: false)
  -s string      System prompt for chat mode (default: "You are a brilliant assistant.")

Configuration:
  -env string    Environment: prod, dev, or custom base URL (default: "dev")
  -u string      User identifier (required)
  -timeout dur   HTTP request timeout (default: 10m)

Logging:
  -logDir string  Directory for log files (default: %q)
  -log-level      Log level: %s (default: %q)

Retry:
  -retries int         Number of retry attempts for failed requests (default: 3)
  -backoff dur         Initial retry backoff time (default: 1s)
  -request-timeout dur Timeout for individual requests (defaults to --timeout value)

Examples:
  # Chat with default model
  echo "Hello, how are you?" | %s

  # Use specific model
  echo "Explain quantum physics" | %s -m claude-3-sonnet

  # Generate embeddings
  echo "Text to embed" | %s -e -m text-embedding-ada-002

  # Stream chat response
  echo "Tell me a story" | %s -stream
`,
		os.Args[0],
		DefaultChatModel, DefaultEmbedModel,
		strings.Join(ChatModels, ", "),
		strings.Join(EmbedModels, ", "),
		defaultLogDir,
		strings.Join(LogLevels, ", "),
		DefaultLogLevel,
		os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}
