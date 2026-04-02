package config

import (
	"flag"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"os"
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
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	// Set custom usage function
	fs.Usage = func() {
		printUsage()
	}

	registerFlags(fs, &cfg)

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	explicit := applyExplicitFlags(fs, &cfg)
	if err := applyEmbedModeDefaults(&cfg, explicit); err != nil {
		return cfg, err
	}

	if err := validateEnvironmentValue(cfg.ArgoEnv); err != nil {
		return cfg, err
	}

	if err := validateModeFlagCombinations(cfg); err != nil {
		return cfg, err
	}

	if err := validateSessionFlagCombinations(cfg); err != nil {
		return cfg, err
	}

	if err := normalizeAndValidateProvider(&cfg); err != nil {
		return cfg, err
	}

	if err := validateProviderCredentials(cfg); err != nil {
		return cfg, err
	}

	if err := validateToolFlags(cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func registerFlags(fs *flag.FlagSet, cfg *Config) {
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
}

// printUsage prints a custom usage message
func printUsage() {
	var cfg Config
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	registerFlags(fs, &cfg)

	fmt.Fprintf(os.Stderr, `Usage: %s [options] < input

lmc is a command-line interface for AI model interactions.

Options:
`, os.Args[0])
	fs.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
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
