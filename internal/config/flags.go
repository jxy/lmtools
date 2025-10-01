package config

import (
	"flag"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"os"
	"strings"
	"time"
)

func getDefaultUser() string {
	return "" // No default, always require -u flag
}

type Config struct {
	// Tool execution settings
	MaxToolRounds       int           `json:"max_tool_rounds,omitempty"`
	MaxToolParallel     int           `json:"max_tool_parallel,omitempty"`
	Model               string        // model to use
	Embed               bool          // whether to run in embed mode
	StreamChat          bool          // whether to use streaming chat mode
	ArgoUser            string        // user identifier (required for Argo provider only)
	System              string        // system prompt for chat
	SystemExplicitlySet bool          // whether -s flag was explicitly provided
	ArgoEnv             string        // environment (prod|dev|custom base URL)
	Timeout             time.Duration // HTTP request timeout
	Retries             int           // number of retry attempts
	Resume              string        // session ID or path to continue
	Branch              string        // message ID to branch from
	ShowSessions        bool          // display conversation trees
	NoSession           bool          // disable session creation
	Delete              string        // node path to delete
	Show                string        // show session or message by ID/path
	SessionsDir         string        // custom sessions directory
	LogDir              string        // custom log directory
	LogLevel            string        // log level (DEBUG, INFO, WARN, ERROR)
	SkipFlockCheck      bool          // skip file locking check

	// Provider support
	Provider    string // provider: argo (default), openai, google, anthropic
	ProviderURL string // custom provider API endpoint
	APIKeyFile  string // path to API key file (required for non-Argo providers)
	ListModels  bool   // list available models from provider

	// Tool support
	EnableTool         bool          // enable built-in universal_command tool
	ToolTimeout        time.Duration // timeout for tool execution (default: 30s)
	ToolWhitelist      string        // path to whitelist file (one command per line)
	ToolBlacklist      string        // path to blacklist file (one command per line)
	ToolAutoApprove    bool          // skip manual approval for whitelisted tools
	ToolNonInteractive bool          // run in non-interactive mode (deny unapproved commands)
	ToolMaxOutputBytes int           // maximum output size per tool execution (default: 1MB)
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
	fs.StringVar(&cfg.System, "s", prompts.DefaultSystemPrompt, "system prompt for chat mode")

	// Tool Options
	fs.BoolVar(&cfg.EnableTool, "tool", false, "enable built-in universal_command tool")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", core.DefaultToolTimeout, "timeout for tool execution")
	fs.StringVar(&cfg.ToolWhitelist, "tool-whitelist", "", "path to whitelist file (one command per line, or JSON arrays for multi-arg commands)")
	fs.StringVar(&cfg.ToolBlacklist, "tool-blacklist", "", "path to blacklist file (one command per line)")
	fs.BoolVar(&cfg.ToolAutoApprove, "tool-auto-approve", false, "skip manual approval for whitelisted tools")
	fs.BoolVar(&cfg.ToolNonInteractive, "tool-non-interactive", false, "run in non-interactive mode (deny unapproved commands)")
	fs.IntVar(&cfg.MaxToolRounds, "max-tool-rounds", core.DefaultMaxToolRounds, "maximum rounds of tool execution")
	fs.IntVar(&cfg.MaxToolParallel, "max-tool-parallel", core.DefaultMaxToolParallel, "maximum parallel tool executions (default: 4)")
	fs.IntVar(&cfg.ToolMaxOutputBytes, "tool-max-output-bytes", int(core.DefaultMaxOutputSize), "maximum output size per tool execution (default: 1MB)")

	// Configuration
	fs.StringVar(&cfg.ArgoEnv, "argo-env", "dev", "environment: prod, dev, or custom base URL")
	fs.StringVar(&cfg.ArgoUser, "argo-user", getDefaultUser(), "user identifier (required for Argo provider)")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

	// Provider support
	fs.StringVar(&cfg.Provider, "provider", constants.ProviderArgo, "provider: argo, openai, google, anthropic")
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
	fs.StringVar(&cfg.LogDir, "log-dir", "", "custom log directory (default: ~/.lmc/logs)")
	fs.StringVar(&cfg.LogLevel, "log-level", "INFO", "log level (DEBUG, INFO, WARN, ERROR)")
	fs.BoolVar(&cfg.SkipFlockCheck, "skip-flock-check", false, "skip file locking check")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	// Use Visit to check which flags were explicitly provided
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "s":
			cfg.SystemExplicitlySet = true
		case "no-session":
			noSessionExplicit = true
		}
	})

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
		return cfg, fmt.Errorf(prompts.ErrEmbedWithStream)
	}

	// Check tool flag combinations
	if cfg.Embed && cfg.EnableTool {
		return cfg, fmt.Errorf(prompts.ErrEmbedWithTool)
	}

	// Check embed mode with session flags
	if cfg.Embed && (cfg.Resume != "" || cfg.Branch != "") {
		return cfg, fmt.Errorf(prompts.ErrEmbedWithSession)
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
	validProviders := []string{constants.ProviderArgo, constants.ProviderOpenAI, constants.ProviderGoogle, constants.ProviderAnthropic}
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
		cfg.Provider = constants.ProviderArgo
	}

	// Provider-specific authentication requirements
	if cfg.Provider == constants.ProviderArgo {
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

	// Validate provider+tool combinations using centralized validation
	if cfg.EnableTool {
		if err := core.ValidateToolSupport(cfg.Provider, cfg.Model); err != nil {
			return cfg, err
		}
	}

	// Validate non-interactive tool mode
	if cfg.EnableTool && cfg.ToolNonInteractive && !cfg.ToolAutoApprove && cfg.ToolWhitelist == "" {
		return cfg, fmt.Errorf("tool-non-interactive requires either -tool-auto-approve or a -tool-whitelist file")
	}

	return cfg, nil
}

// printUsage prints a custom usage message
func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] < input

lmc is a command-line interface for AI model interactions.

Model Options:
  -model string  Model to use (default varies by provider: gpt5 for argo, gpt-5 for openai,
                 claude-opus-4-1-20250805 for anthropic, gemini-2.5-pro for google)
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
  -provider string       Provider: argo, openai, google, anthropic (default: "argo")
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
  -log-dir string    Custom log directory (default: ~/.lmc/logs)
  -log-level string  Log level (DEBUG, INFO, WARN, ERROR) (default: "INFO")
  -skip-flock-check  Skip file locking check

Tool Options:
  -tool                 Enable built-in universal_command tool
  -tool-timeout duration Timeout for tool execution (default: 30s)
  -tool-whitelist string Path to whitelist file (one command per line, or JSON arrays for multi-arg commands)
  -tool-blacklist string Path to blacklist file (one command per line)
  -tool-auto-approve    Skip manual approval for whitelisted tools
  -tool-non-interactive Run in non-interactive mode (deny unapproved commands)
  -max-tool-rounds int  Maximum rounds of tool execution (default: 32)
  -max-tool-parallel int Maximum parallel tool executions (default: 4)
  -tool-max-output-bytes int Maximum output size per tool execution (default: 1MB)

Examples:
  # Chat with default Argo provider
  echo "Hello, how are you?" | %s -argo-user myuser

  # Use OpenAI provider with API key
  echo "Explain quantum physics" | %s -provider openai -api-key-file ~/.openai-key

  # Use Google provider 
  echo "Tell me about AI" | %s -provider google -api-key-file ~/.google-key

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

func (c Config) IsSystemExplicitlySet() bool {
	return c.SystemExplicitlySet
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

func (c Config) IsToolEnabled() bool {
	return c.EnableTool
}

// GetEffectiveSystem returns the appropriate system prompt:
// - If tool is enabled and no system prompt was explicitly set, returns the tool system prompt
// - Otherwise returns the configured system prompt
func (c Config) GetEffectiveSystem() string {
	// If tool is enabled via -tool flag and system wasn't explicitly set, use tool prompt
	if c.EnableTool && !c.SystemExplicitlySet {
		return prompts.ToolSystemPrompt
	}
	return c.System
}

func (c Config) GetToolTimeout() time.Duration {
	if c.ToolTimeout <= 0 {
		return core.DefaultToolTimeout
	}
	return c.ToolTimeout
}

func (c Config) GetToolWhitelist() string {
	return c.ToolWhitelist
}

func (c Config) GetToolBlacklist() string {
	return c.ToolBlacklist
}

func (c Config) GetToolAutoApprove() bool {
	return c.ToolAutoApprove
}

func (c Config) GetToolNonInteractive() bool {
	return c.ToolNonInteractive
}

func (c Config) GetMaxToolRounds() int {
	if c.MaxToolRounds <= 0 {
		return core.DefaultMaxToolRounds
	}
	return c.MaxToolRounds
}

func (c Config) GetMaxToolParallel() int {
	if c.MaxToolParallel <= 0 {
		return 4 // Default to 4 parallel executions
	}
	return c.MaxToolParallel
}

func (c Config) GetToolMaxOutputBytes() int {
	if c.ToolMaxOutputBytes <= 0 {
		return int(core.DefaultMaxOutputSize)
	}
	// Add upper bound validation (100MB)
	const maxAllowed = 100 * 1024 * 1024 // 100MB
	if c.ToolMaxOutputBytes > maxAllowed {
		return maxAllowed
	}
	return c.ToolMaxOutputBytes
}

// GetResume returns the resume session ID/path
func (c Config) GetResume() string {
	return c.Resume
}

// GetBranch returns the branch message ID
func (c Config) GetBranch() string {
	return c.Branch
}
