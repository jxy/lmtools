package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/prompts"
	"lmtools/internal/providerconfig"
	"lmtools/internal/retry"
	"os"
	"strings"
	"time"
)

type Config struct {
	Context contextcfg.Binding
	// Tool execution settings
	MaxToolRounds    int    `json:"max_tool_rounds,omitempty"`
	MaxToolParallel  int    `json:"max_tool_parallel,omitempty"`
	Model            string // model to use
	Embed            bool   // whether to run in embed mode
	NoStream         bool   // -no-stream: wait for the whole answer
	StreamChat       bool   // whether to use streaming chat mode; ParseFlags derives it from NoStream
	REPL             bool   // read turns until the end of input, continuing one conversation
	PrintCurl        bool   // print the equivalent curl command instead of sending the request
	HideThinking     bool   // -hide-thinking: leave thinking summaries off the display
	ShowThinking     bool   // print visible provider-returned thinking summaries to stderr; ParseFlags derives it from HideThinking
	Effort           string // reasoning effort hint
	ReasoningMode    string // OpenAI Responses reasoning.mode (standard, pro)
	ReasoningContext string // OpenAI Responses reasoning.context (auto, current_turn, all_turns)
	MaxTokens        int    // maximum output tokens (0 = provider default)
	JSONMode         bool   // request JSON object output
	JSONSchemaPath   string // path to JSON schema for structured output
	JSONSchema       json.RawMessage
	ImagePaths       []string          // -image arguments in command-line order
	ImageDetail      string            // OpenAI detail hint applied to every -image
	Images           []core.ImageBlock // -image files loaded at parse time as data URLs
	providerconfig.Options
	OpenAIResponses     bool          // chat through the provider's Responses API where the model has one; ParseFlags derives it from ChatCompletions
	System              string        // system prompt for chat
	SystemExplicitlySet bool          // whether -s flag was explicitly provided
	Developer           string        // pending developer instruction for the first new user turn
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
	Version    bool // print version information and exit

	// Tool support
	EnableTool            bool // enable the built-in universal_command and view_image tools
	implicitNotebookTools bool
	ToolTimeout           time.Duration // timeout for tool execution
	ToolWhitelist         string        // JSON command rules that run without prompting
	ToolBlacklist         string        // JSON command rules that are always denied
	ToolAutoApprove       bool          // run unless denied by blacklist or restrictive whitelist
	ToolNonInteractive    bool          // never prompt; deny commands not approved by policy
	ToolMaxOutputBytes    int           // maximum output size per tool execution (default: 1MB)

	// MCP servers
	MCPConfigPaths []string           // -mcp-config files in command-line order
	MCPServers     []mcp.ServerConfig // servers those files configure, loaded at parse time, less what the selection drops
	MCPWarnings    []string           // what the loader noted, for the operator

	// Tool selection
	ToolInclude   []string // -tool-include selectors in command-line order
	ToolExclude   []string // -tool-exclude selectors in command-line order
	ExcludedTools []string // built-in tools the selection withholds
}

func ParseFlags(args []string) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	// Set custom usage function
	fs.Usage = func() {
		printUsage()
	}

	registerFlags(fs, &cfg)
	removedFlags.Register(fs)

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if err := removedFlags.Err(fs); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		return cfg, unexpectedArgumentError(fs.Arg(0))
	}

	// -version is answered before any validation, like -h: it needs no
	// provider, credentials, or input.
	if cfg.Version {
		return cfg, nil
	}

	explicit := applyExplicitFlags(fs, &cfg)
	if err := validateREPLFlags(cfg); err != nil {
		return cfg, err
	}
	if err := applyEmbedModeDefaults(&cfg, explicit); err != nil {
		return cfg, err
	}
	applyModeDefaults(&cfg)

	if err := validateModeFlagCombinations(cfg); err != nil {
		return cfg, err
	}

	if err := validateSessionFlagCombinations(cfg); err != nil {
		return cfg, err
	}

	if err := validateOutputFlags(&cfg); err != nil {
		return cfg, err
	}

	if err := validateImageFlags(&cfg); err != nil {
		return cfg, err
	}

	if err := cfg.Normalize(); err != nil {
		return cfg, err
	}
	if err := validateDeveloperFlags(cfg); err != nil {
		return cfg, err
	}

	if err := validateProviderCredentials(cfg); err != nil {
		return cfg, err
	}

	if err := validateMCPFlags(&cfg); err != nil {
		return cfg, err
	}
	if err := configureContext(&cfg, fs); err != nil {
		return cfg, err
	}

	if err := validateToolSelection(&cfg); err != nil {
		return cfg, err
	}

	if err := validateToolFlags(cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func registerFlags(fs *flag.FlagSet, cfg *Config) {
	registerContextFlags(fs, cfg)
	// Model Options
	fs.StringVar(&cfg.Model, "model", "", fmt.Sprintf("model to use (default varies by provider, %q for embed)",
		core.DefaultEmbedModel))
	fs.BoolVar(&cfg.Embed, "e", false, "enable embed mode instead of chat")

	// Chat Options
	fs.BoolVar(&cfg.NoStream, "no-stream", false, "wait for the whole answer instead of streaming it")
	fs.BoolVar(&cfg.REPL, "repl", false, "read one turn per line from stdin until it ends, continuing one session")
	fs.BoolVar(&cfg.PrintCurl, "print-curl", false, "print the equivalent curl command and exit without sending the request")
	fs.BoolVar(&cfg.HideThinking, "hide-thinking", false, "hide the provider-returned thinking summaries lmc prints to stderr; the request and its reasoning are unchanged")
	fs.StringVar(&cfg.System, "s", prompts.DefaultSystemPrompt, "system prompt for chat mode")
	fs.StringVar(&cfg.Developer, "developer", "", "developer instruction saved before the first new user turn (OpenAI, or Argo gpt* without -chat-completions)")
	fs.StringVar(&cfg.Effort, "effort", "", "reasoning effort hint: none, minimal, low, medium, high, xhigh, max")
	fs.StringVar(&cfg.ReasoningMode, "reasoning-mode", "", "Responses API reasoning mode: standard, pro (pro is GPT-5.6 only)")
	fs.StringVar(&cfg.ReasoningContext, "reasoning-context", "", "Responses API reasoning context: auto, current_turn, all_turns")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", 0, "maximum output tokens (0 uses provider default; Claude defaults to 128000 for Opus, 64000 otherwise)")
	fs.BoolVar(&cfg.JSONMode, "json", false, "request JSON object output")
	fs.StringVar(&cfg.JSONSchemaPath, "json-schema", "", "path to JSON schema file passed to the provider for structured output")
	fs.Var(imagePathsFlag{paths: &cfg.ImagePaths}, "image", "attach an image file ("+core.SupportedImageMediaTypesText+") to the prompt; repeatable")
	fs.StringVar(&cfg.ImageDetail, "image-detail", "", "OpenAI detail hint for every -image: auto, low, high")

	// Tool Options
	fs.BoolVar(&cfg.EnableTool, "tool", false, "enable universal_command with direct execvpe-style execution (no shell) and view_image, which returns an image file to the model")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", core.DefaultToolTimeout, "timeout per command")
	fs.StringVar(&cfg.ToolWhitelist, "tool-whitelist", "", "path to JSON command-rule whitelist; redirected calls require exact object rules")
	fs.StringVar(&cfg.ToolBlacklist, "tool-blacklist", "", "path to JSON command-rule blacklist; matches are always denied")
	fs.BoolVar(&cfg.ToolAutoApprove, "tool-auto-approve", false, "run without prompting unless denied by blacklist or non-interactive whitelist")
	fs.BoolVar(&cfg.ToolNonInteractive, "tool-non-interactive", false, "never prompt; with a whitelist, deny non-matching commands")
	fs.IntVar(&cfg.MaxToolRounds, "max-tool-rounds", core.DefaultMaxToolRounds, "tool execution rounds per block before interactive confirmation")
	fs.IntVar(&cfg.MaxToolParallel, "max-tool-parallel", core.DefaultMaxToolParallel, "maximum concurrent command executions")
	fs.IntVar(&cfg.ToolMaxOutputBytes, "tool-max-output-bytes", int(core.DefaultMaxOutputSize), "maximum captured output bytes per command")
	fs.Var(mcpConfigFlag{paths: &cfg.MCPConfigPaths}, "mcp-config", "path to an mcpServers JSON file whose servers' tools are advertised beside the built-in ones; implies -tool; repeatable")
	fs.Var(selectorFlag{flag: "-tool-include", names: &cfg.ToolInclude}, "tool-include", "advertise only these tools: universal_command, view_image, an MCP server, or server/tool; comma separated, repeatable")
	fs.Var(selectorFlag{flag: "-tool-exclude", names: &cfg.ToolExclude}, "tool-exclude", "withhold these tools, in the same form as -tool-include; an excluded server is never started")

	// Configuration
	providerconfig.RegisterFlags(fs, &cfg.Options, providerconfig.Defaults{
		Provider: constants.ProviderArgo,
		ArgoUser: "",
	})
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

	fs.BoolVar(&cfg.ListModels, "list-models", false, "list available models from provider")
	fs.BoolVar(&cfg.Version, "version", false, "print version information and exit")

	// Retry configuration
	fs.IntVar(&cfg.Retries, "retries", retry.DefaultMaxRetries, "number of retry attempts for failed requests")

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

  # Wait for the whole answer instead of streaming it, without the thinking
  echo "Tell me a story" | %s -argo-user myuser -no-stream -hide-thinking

  # Ask about an image (repeat -image to attach several)
  echo "What is in this picture?" | %s -argo-user myuser -image photo.png

  # Resume a session
  echo "Continue from where we left off" | %s -argo-user myuser -resume 001a

  # Hold a conversation, one turn per line, until Ctrl-D
  %s -argo-user myuser -repl

  # Show all conversation trees
  %s -argo-user myuser -show-sessions
`,
		os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

// RequestOptions converts parsed CLI flags into the concrete value consumed by
// core/session code.
func (c Config) RequestOptions() core.RequestOptions {
	effectiveSystem := c.System
	if c.EnableTool && !c.SystemExplicitlySet {
		effectiveSystem = core.ToolSystemPromptFor(c.ExcludedTools)
	}

	argoEnv := c.ArgoEnv
	if argoEnv == "" {
		argoEnv = providerconfig.ResolveArgoEnvironment(c.ArgoDev, c.ArgoTest)
	}

	return core.RequestOptions{
		Context:               c.Context,
		User:                  c.ArgoUser,
		Model:                 c.Model,
		System:                c.System,
		EffectiveSystem:       effectiveSystem,
		SystemExplicitlySet:   c.SystemExplicitlySet,
		Developer:             c.Developer,
		Env:                   argoEnv,
		ArgoLegacy:            c.ArgoLegacy,
		Embed:                 c.Embed,
		StreamChat:            c.StreamChat,
		Provider:              c.Provider,
		ProviderURL:           c.ProviderURL,
		APIKeyFile:            c.APIKeyFile,
		Effort:                c.Effort,
		ReasoningMode:         c.ReasoningMode,
		ReasoningContext:      c.ReasoningContext,
		MaxTokens:             c.MaxTokens,
		JSONMode:              c.JSONMode,
		JSONSchema:            append(json.RawMessage(nil), c.JSONSchema...),
		OpenAIResponses:       c.OpenAIResponses,
		AnthropicBearer:       c.AnthropicBearer,
		ToolEnabled:           c.EnableTool,
		ImplicitNotebookTools: c.implicitNotebookTools,
		ToolTimeout:           c.ToolTimeout,
		ToolWhitelist:         c.ToolWhitelist,
		ToolBlacklist:         c.ToolBlacklist,
		ToolAutoApprove:       c.ToolAutoApprove,
		ToolNonInteractive:    c.ToolNonInteractive,
		MaxToolRounds:         c.MaxToolRounds,
		MaxToolParallel:       c.MaxToolParallel,
		ToolMaxOutputBytes:    c.ToolMaxOutputBytes,
		Resume:                c.Resume,
		Branch:                c.Branch,
		Images:                append([]core.ImageBlock(nil), c.Images...),
		ExcludedTools:         append([]string(nil), c.ExcludedTools...),
	}
}

// removedFlags answers the mode flags lmc replaced when their modes became
// the defaults.
var removedFlags = providerconfig.RemovedFlags{
	"stream":           "lmc streams by default, and -no-stream waits for the whole answer",
	"show-thinking":    "lmc shows thinking by default, and -hide-thinking hides it",
	"openai-responses": "lmc uses the Responses API by default where the provider has one, and -chat-completions uses chat/completions",
}

// unexpectedArgumentError refuses a word the flag parser could not place. The
// parser stops at the first such word and leaves every flag after it unread,
// so `-hide-thinking false -repl` would otherwise run without -repl and say
// nothing.
func unexpectedArgumentError(arg string) error {
	switch strings.ToLower(arg) {
	case "true", "false", "t", "f", "1", "0":
		return fmt.Errorf("unexpected argument %q: give a boolean flag its value with =, as in -hide-thinking=false", arg)
	}
	return fmt.Errorf("unexpected argument %q: lmc reads the prompt from standard input, as in echo 'question' | lmc", arg)
}
