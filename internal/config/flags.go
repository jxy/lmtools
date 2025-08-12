package config

import (
	"flag"
	"fmt"
	"lmtools/internal/core"
	"os"
	"strings"
	"time"
)

// LogDir configuration removed - logs now always go to ~/.lmc/logs

func getDefaultUser() string {
	return "" // No default, always require -u flag
}

type Config struct {
	Model          string        // model to use
	Embed          bool          // whether to run in embed mode
	StreamChat     bool          // whether to use streaming chat mode
	ArgoUser       string        // user identifier (required for Argo provider only)
	System         string        // system prompt for chat
	ArgoEnv        string        // environment (prod|dev|custom base URL)
	Timeout        time.Duration // HTTP request timeout
	Retries        int           // number of retry attempts
	Resume         string        // session ID or path to continue
	Branch         string        // message ID to branch from
	ShowSessions   bool          // display conversation trees
	NoSession      bool          // disable session creation
	Delete         string        // node path to delete
	Show           string        // show session or message by ID/path
	SessionsDir    string        // custom sessions directory
	SkipFlockCheck bool          // skip file locking check

	// Provider support
	Provider    string // provider: argo (default), openai, gemini, anthropic
	ProviderURL string // custom provider API endpoint
	APIKeyFile  string // path to API key file (required for non-Argo providers)
	ListModels  bool   // list available models from provider
}

func ParseFlags(args []string) (Config, error) {
	var cfg Config
	var noSessionExplicit bool
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	// Set custom usage function
	fs.Usage = func() {
		printUsage()
	}

	// Model Options
	fs.StringVar(&cfg.Model, "model", "", fmt.Sprintf("model to use (default varies by provider, %q for embed)",
		core.DefaultEmbedModel))
	fs.BoolVar(&cfg.Embed, "e", false, "enable embed mode instead of chat")

	// Chat Options
	fs.BoolVar(&cfg.StreamChat, "stream", false, "use streaming chat mode")
	fs.StringVar(&cfg.System, "s", "You are a brilliant assistant.", "system prompt for chat mode")

	// Configuration
	fs.StringVar(&cfg.ArgoEnv, "argo-env", "dev", "environment: prod, dev, or custom base URL")
	fs.StringVar(&cfg.ArgoUser, "argo-user", getDefaultUser(), "user identifier (required for Argo provider)")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

	// Provider support
	fs.StringVar(&cfg.Provider, "provider", "argo", "provider: argo, openai, gemini, anthropic")
	fs.StringVar(&cfg.ProviderURL, "provider-url", "", "custom provider API endpoint")
	fs.StringVar(&cfg.APIKeyFile, "api-key-file", "", "path to API key file (required for non-Argo providers)")
	fs.BoolVar(&cfg.ListModels, "list-models", false, "list available models from provider")

	// Retry configuration
	fs.IntVar(&cfg.Retries, "retries", 3, "number of retry attempts for failed requests")

	// Session Options
	fs.StringVar(&cfg.Resume, "resume", "", "resume session or branch by ID/path")
	fs.StringVar(&cfg.Branch, "branch", "", "create branch from message ID")
	fs.BoolVar(&cfg.ShowSessions, "show-sessions", false, "display all conversation trees")
	fs.BoolVar(&cfg.NoSession, "no-session", false, "disable session creation")
	fs.StringVar(&cfg.Delete, "delete", "", "delete node and its descendants")
	fs.StringVar(&cfg.Show, "show", "", "show session or message by ID/path")
	fs.StringVar(&cfg.SessionsDir, "sessions-dir", "", "custom sessions directory (default: ~/.lmc/sessions)")
	fs.BoolVar(&cfg.SkipFlockCheck, "skip-flock-check", false, "skip file locking check")

	// Check if -no-session was explicitly set before parsing
	for i := 0; i < len(args); i++ {
		if args[i] == "-no-session" || strings.HasPrefix(args[i], "-no-session=") {
			noSessionExplicit = true
			break
		}
	}

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	// Check for invalid flag combination: embed mode with explicit -no-session=false
	if cfg.Embed && noSessionExplicit && !cfg.NoSession {
		return cfg, fmt.Errorf("invalid flag combination: embed mode requires sessions to be disabled. Remove -no-session=false or use chat mode instead")
	}

	// Automatically disable sessions in embed mode (only if not explicitly set)
	if cfg.Embed && !noSessionExplicit {
		cfg.NoSession = true
	}

	// Validate environment
	if !IsValidEnvironment(cfg.ArgoEnv) {
		return cfg, fmt.Errorf("invalid argo-env: %q, must be one of: %s, or a custom URL (http://... or https://...)",
			cfg.ArgoEnv, strings.Join(Environments, ", "))
	}

	// Check invalid flag combinations
	if cfg.Embed && cfg.StreamChat {
		return cfg, fmt.Errorf("invalid flag combination: embed mode cannot be used with stream")
	}

	// Check embed mode with session flags
	if cfg.Embed && (cfg.Resume != "" || cfg.Branch != "") {
		return cfg, fmt.Errorf("invalid flag combination: embed mode cannot be used with session flags (-resume, -branch)")
	}

	// Check session flag combinations
	if cfg.ShowSessions && (cfg.Resume != "" || cfg.Branch != "" || cfg.NoSession || cfg.Delete != "" || cfg.Show != "") {
		return cfg, fmt.Errorf("invalid flag combination: -show-sessions cannot be used with other session flags")
	}

	if cfg.Delete != "" && (cfg.Resume != "" || cfg.Branch != "" || cfg.NoSession || cfg.Show != "") {
		return cfg, fmt.Errorf("invalid flag combination: -delete cannot be used with other session flags")
	}

	if cfg.Show != "" && (cfg.Resume != "" || cfg.Branch != "" || cfg.Delete != "" || cfg.ShowSessions || cfg.NoSession || cfg.Embed || cfg.StreamChat) {
		return cfg, fmt.Errorf("invalid flag combination: -show cannot be used with other session or operation flags")
	}

	if cfg.Resume != "" && cfg.Branch != "" {
		return cfg, fmt.Errorf("invalid flag combination: -resume and -branch cannot be used together")
	}

	if cfg.NoSession && (cfg.Resume != "" || cfg.Branch != "") {
		return cfg, fmt.Errorf("invalid flag combination: -no-session cannot be used with -resume or -branch")
	}

	// Provider-specific validation
	// Normalize provider to lowercase
	cfg.Provider = strings.ToLower(cfg.Provider)

	// Validate provider
	validProviders := []string{"argo", "openai", "gemini", "anthropic"}
	isValidProvider := false
	for _, p := range validProviders {
		if cfg.Provider == p {
			isValidProvider = true
			break
		}
	}
	if !isValidProvider && cfg.Provider != "" {
		return cfg, fmt.Errorf("invalid provider: %q, must be one of: %s",
			cfg.Provider, strings.Join(validProviders, ", "))
	}

	// Default to argo if not specified
	if cfg.Provider == "" {
		cfg.Provider = "argo"
	}

	// Provider-specific authentication requirements
	if cfg.Provider == "argo" {
		// Argo requires user identifier (except for show-sessions, delete, show, and list-models)
		if cfg.ArgoUser == "" && !cfg.ShowSessions && cfg.Delete == "" && cfg.Show == "" && !cfg.ListModels {
			return cfg, fmt.Errorf("user identifier (-argo-user) is required for Argo provider")
		}
	} else {
		// For non-Argo providers using standard endpoints (no custom URL), require API key file
		if cfg.ProviderURL == "" && cfg.APIKeyFile == "" && !cfg.ShowSessions && cfg.Delete == "" && cfg.Show == "" && !cfg.ListModels {
			return cfg, fmt.Errorf("-api-key-file is required for %s provider when not using custom -provider-url", cfg.Provider)
		}
		// For custom provider URLs, API key is optional (might be an internal service)
		// For non-Argo providers, if no user is specified, we'll use a hash of the API key later
	}

	return cfg, nil
}

// printUsage prints a custom usage message
func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] < input

lmc is a command-line interface for AI model interactions.

Model Options:
  -model string  Model to use (default varies by provider: gpt5 for argo, gpt-5 for openai,
                 claude-opus-4-1-20250805 for anthropic, gemini-2.5-pro for gemini)
                 Use -list-models to see available models for your provider
  -e             Enable embed mode instead of chat (default: false)

Chat Options:
  -stream        Use streaming chat mode (default: false)
  -s string      System prompt for chat mode (default: "You are a brilliant assistant.")

Configuration:
  -argo-env string   Environment: prod, dev, or custom base URL (default: "dev")
  -argo-user string  User identifier (required for Argo provider only)
  -timeout dur       HTTP request timeout (default: 10m)

Provider Options:
  -provider string       Provider: argo, openai, gemini, anthropic (default: "argo")
  -provider-url string   Custom provider API base URL with version (e.g., http://localhost:8080/v1)
  -api-key-file string   Path to API key file (required for standard non-Argo providers)
  -list-models           List available models from the provider

Retry:
  -retries int    Number of retry attempts for failed requests (default: 3)

Session Options:
  -resume string      Resume session or branch by ID/path
  -branch string      Create branch from message ID
  -show-sessions      Display all conversation trees
  -no-session        Disable session creation
  -delete string      Delete node and its descendants
  -show string        Show session or message by ID/path
  -sessions-dir string Custom sessions directory (default: ~/.lmc/sessions)
  -skip-flock-check  Skip file locking check

Examples:
  # Chat with default Argo provider
  echo "Hello, how are you?" | %s -argo-user myuser

  # Use OpenAI provider with API key
  echo "Explain quantum physics" | %s -provider openai -api-key-file ~/.openai-key

  # Use Gemini provider 
  echo "Tell me about AI" | %s -provider gemini -api-key-file ~/.gemini-key

  # Use Anthropic provider with specific model
  echo "Write a poem" | %s -provider anthropic -api-key-file ~/.anthropic-key -model claude-3-opus-20240229

  # Use custom provider endpoint (no API key required)
  echo "Hello" | %s -provider openai -provider-url http://localhost:8080/v1

  # Stream chat response
  echo "Tell me a story" | %s -argo-user myuser -stream

  # Resume a session
  echo "Continue from where we left off" | %s -argo-user myuser -resume 001a

  # Show all conversation trees
  %s -argo-user myuser -show-sessions
`,
		os.Args[0],
		os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

// Adapter methods for Config to implement core.RequestConfig interface

func (c Config) GetUser() string {
	return c.ArgoUser
}

func (c Config) GetModel() string {
	return c.Model
}

func (c Config) GetSystem() string {
	return c.System
}

func (c Config) GetEnv() string {
	return c.ArgoEnv
}

func (c Config) IsEmbed() bool {
	return c.Embed
}

func (c Config) IsStreamChat() bool {
	return c.StreamChat
}

func (c Config) GetProvider() string {
	return c.Provider
}

func (c Config) GetProviderURL() string {
	return c.ProviderURL
}

func (c Config) GetAPIKeyFile() string {
	return c.APIKeyFile
}
