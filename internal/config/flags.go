package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"lmtools/internal/providerconfig"
	"os"
	"time"
)

type Config struct {
	// Tool execution settings
	MaxToolRounds    int    `json:"max_tool_rounds,omitempty"`
	MaxToolParallel  int    `json:"max_tool_parallel,omitempty"`
	Model            string // model to use
	Embed            bool   // whether to run in embed mode
	StreamChat       bool   // whether to use streaming chat mode
	PrintCurl        bool   // print the equivalent curl command instead of sending the request
	ShowThinking     bool   // print visible provider-returned thinking summaries to stderr
	Effort           string // reasoning effort hint
	ReasoningMode    string // OpenAI Responses reasoning.mode (standard, pro)
	ReasoningContext string // OpenAI Responses reasoning.context (auto, current_turn, all_turns)
	MaxTokens        int    // maximum output tokens (0 = provider default)
	JSONMode         bool   // request JSON object output
	JSONSchemaPath   string // path to JSON schema for structured output
	JSONSchema       json.RawMessage
	providerconfig.Options
	System              string        // system prompt for chat
	SystemExplicitlySet bool          // whether -s flag was explicitly provided
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

	ListModels bool // list available models from provider

	// Tool support
	EnableTool         bool          // enable built-in universal_command tool
	ToolTimeout        time.Duration // timeout for tool execution
	ToolWhitelist      string        // JSON command rules that run without prompting
	ToolBlacklist      string        // JSON command rules that are always denied
	ToolAutoApprove    bool          // run unless denied by blacklist or restrictive whitelist
	ToolNonInteractive bool          // never prompt; deny commands not approved by policy
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

	if err := validateModeFlagCombinations(cfg); err != nil {
		return cfg, err
	}

	if err := validateSessionFlagCombinations(cfg); err != nil {
		return cfg, err
	}

	if err := validateOutputFlags(&cfg); err != nil {
		return cfg, err
	}

	if err := cfg.Normalize(); err != nil {
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
	fs.BoolVar(&cfg.PrintCurl, "print-curl", false, "print the equivalent curl command and exit without sending the request")
	fs.BoolVar(&cfg.ShowThinking, "show-thinking", false, "print provider-returned thinking summaries to stderr (does not enable reasoning)")
	fs.StringVar(&cfg.System, "s", prompts.DefaultSystemPrompt, "system prompt for chat mode")
	fs.StringVar(&cfg.Effort, "effort", "", "reasoning effort hint: none, minimal, low, medium, high, xhigh, max")
	fs.StringVar(&cfg.ReasoningMode, "reasoning-mode", "", "Responses API reasoning mode: standard, pro (requires -openai-responses; pro is GPT-5.6 only)")
	fs.StringVar(&cfg.ReasoningContext, "reasoning-context", "", "Responses API reasoning context: auto, current_turn, all_turns (requires -openai-responses)")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", 0, "maximum output tokens (0 uses provider default; Claude defaults to 128000 for Opus, 64000 otherwise)")
	fs.BoolVar(&cfg.JSONMode, "json", false, "request JSON object output")
	fs.StringVar(&cfg.JSONSchemaPath, "json-schema", "", "path to JSON schema file for structured output")

	// Tool Options
	fs.BoolVar(&cfg.EnableTool, "tool", false, "enable universal_command with direct execvpe-style execution (no shell)")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", core.DefaultToolTimeout, "timeout per command")
	fs.StringVar(&cfg.ToolWhitelist, "tool-whitelist", "", "path to JSON command-rule whitelist; redirected calls require exact object rules")
	fs.StringVar(&cfg.ToolBlacklist, "tool-blacklist", "", "path to JSON command-rule blacklist; matches are always denied")
	fs.BoolVar(&cfg.ToolAutoApprove, "tool-auto-approve", false, "run without prompting unless denied by blacklist or non-interactive whitelist")
	fs.BoolVar(&cfg.ToolNonInteractive, "tool-non-interactive", false, "never prompt; with a whitelist, deny non-matching commands")
	fs.IntVar(&cfg.MaxToolRounds, "max-tool-rounds", core.DefaultMaxToolRounds, "tool execution rounds per block before interactive confirmation")
	fs.IntVar(&cfg.MaxToolParallel, "max-tool-parallel", core.DefaultMaxToolParallel, "maximum concurrent command executions")
	fs.IntVar(&cfg.ToolMaxOutputBytes, "tool-max-output-bytes", int(core.DefaultMaxOutputSize), "maximum captured output bytes per command")

	// Configuration
	providerconfig.RegisterFlags(fs, &cfg.Options, providerconfig.Defaults{
		Provider: constants.ProviderArgo,
		ArgoUser: "",
	})
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

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
		argoEnv = providerconfig.ResolveArgoEnvironment(c.ArgoDev, c.ArgoTest)
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
		Effort:              c.Effort,
		ReasoningMode:       c.ReasoningMode,
		ReasoningContext:    c.ReasoningContext,
		MaxTokens:           c.MaxTokens,
		JSONMode:            c.JSONMode,
		JSONSchema:          append(json.RawMessage(nil), c.JSONSchema...),
		OpenAIResponses:     c.OpenAIResponses,
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
