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
	ArgoUser            string        // user identifier for Argo provider (or use APIKeyFile)
	System              string        // system prompt for chat
	SystemExplicitlySet bool          // whether -s flag was explicitly provided
	ArgoDev             bool          // whether to use the Argo dev environment
	ArgoTest            bool          // whether to use the Argo test environment
	ArgoLegacy          bool          // whether to use legacy Argo chat/streamchat endpoints
	ArgoEnv             string        // resolved environment (prod|dev|test)
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
	APIKeyFile  string // path to API key file (required for openai/google/anthropic; optional for argo)
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

	cfg.ArgoEnv = resolveArgoEnvironment(cfg.ArgoDev, cfg.ArgoTest)

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
	fs.BoolVar(&cfg.ArgoDev, "argo-dev", false, "use the Argo dev environment instead of prod")
	fs.BoolVar(&cfg.ArgoTest, "argo-test", false, "use the Argo test environment instead of prod")
	fs.BoolVar(&cfg.ArgoLegacy, "argo-legacy", false, "use legacy Argo /api/v1/resource chat endpoints")
	fs.StringVar(&cfg.ArgoUser, "argo-user", getDefaultUser(), "user identifier for Argo provider (or use -api-key-file)")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

	// Provider support
	fs.StringVar(&cfg.Provider, "provider", constants.ProviderArgo, "provider: argo, openai, google, anthropic")
	fs.StringVar(&cfg.ProviderURL, "provider-url", "", "custom provider API endpoint")
	fs.StringVar(&cfg.APIKeyFile, "api-key-file", "", "path to API key file (required for openai/google/anthropic; optional alternative for argo)")
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

// RequestOptions converts parsed CLI flags into the concrete value consumed by
// core/session code.
func (c Config) RequestOptions() core.RequestOptions {
	effectiveSystem := c.System
	if c.EnableTool && !c.SystemExplicitlySet {
		effectiveSystem = prompts.ToolSystemPrompt
	}

	argoEnv := c.ArgoEnv
	if argoEnv == "" {
		argoEnv = resolveArgoEnvironment(c.ArgoDev, c.ArgoTest)
	}

	return core.RequestOptions{
		User:                c.ArgoUser,
		Model:               c.Model,
		System:              c.System,
		EffectiveSystem:     effectiveSystem,
		SystemExplicitlySet: c.SystemExplicitlySet,
		Env:                 argoEnv,
		ArgoLegacy:          c.ArgoLegacy,
		Embed:               c.Embed,
		StreamChat:          c.StreamChat,
		Provider:            c.Provider,
		ProviderURL:         c.ProviderURL,
		APIKeyFile:          c.APIKeyFile,
		ToolEnabled:         c.EnableTool,
		ToolTimeout:         c.ToolTimeout,
		ToolWhitelist:       c.ToolWhitelist,
		ToolBlacklist:       c.ToolBlacklist,
		ToolAutoApprove:     c.ToolAutoApprove,
		ToolNonInteractive:  c.ToolNonInteractive,
		MaxToolRounds:       c.MaxToolRounds,
		MaxToolParallel:     c.MaxToolParallel,
		ToolMaxOutputBytes:  c.ToolMaxOutputBytes,
		Resume:              c.Resume,
		Branch:              c.Branch,
	}
}
