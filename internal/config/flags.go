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
	User           string        // user identifier
	System         string        // system prompt for chat
	Env            string        // environment (prod|dev|custom base URL)
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
	fs.StringVar(&cfg.Model, "m", "", fmt.Sprintf("model to use (default: %q for chat, %q for embed)\n\t\tChat models: %s\n\t\tEmbed models: %s",
		core.DefaultChatModel, core.DefaultEmbedModel,
		strings.Join(core.ChatModels, ", "),
		strings.Join(core.EmbedModels, ", ")))
	fs.BoolVar(&cfg.Embed, "e", false, "enable embed mode instead of chat")

	// Chat Options
	fs.BoolVar(&cfg.StreamChat, "stream", false, "use streaming chat mode")
	fs.StringVar(&cfg.System, "s", "You are a brilliant assistant.", "system prompt for chat mode")

	// Configuration
	fs.StringVar(&cfg.Env, "env", "dev", "environment: prod, dev, or custom base URL")
	fs.StringVar(&cfg.User, "u", getDefaultUser(), "user identifier")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "HTTP request timeout")

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
	if !IsValidEnvironment(cfg.Env) {
		return cfg, fmt.Errorf("invalid env: %q, must be one of: %s, or a custom URL (http://... or https://...)",
			cfg.Env, strings.Join(Environments, ", "))
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

	// Validate user is provided (except for show-sessions, delete, and show)
	if cfg.User == "" && !cfg.ShowSessions && cfg.Delete == "" && cfg.Show == "" {
		return cfg, fmt.Errorf("user identifier (-u) is required")
	}

	return cfg, nil
}

// printUsage prints a custom usage message
func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] < input

lmc is a command-line interface for AI model interactions.

Model Options:
  -m string      Model to use (default: %q for chat, %q for embed)
                 Chat models: %s
                 Embed models: %s
  -e             Enable embed mode instead of chat (default: false)

Chat Options:
  -stream        Use streaming chat mode (default: false)
  -s string      System prompt for chat mode (default: "You are a brilliant assistant.")

Configuration:
  -env string    Environment: prod, dev, or custom base URL (default: "dev")
  -u string      User identifier (required)
  -timeout dur   HTTP request timeout (default: 10m)

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
  # Chat with default model
  echo "Hello, how are you?" | %s

  # Use specific model
  echo "Explain quantum physics" | %s -m claude-3-sonnet

  # Generate embeddings
  echo "Text to embed" | %s -e -m text-embedding-ada-002

  # Stream chat response
  echo "Tell me a story" | %s -stream

  # Resume a session
  echo "Continue from where we left off" | %s -resume 001a

  # Branch from a message
  echo "Let me rephrase that" | %s -branch 001a/0002

  # Show all conversation trees
  %s -show-sessions
`,
		os.Args[0],
		core.DefaultChatModel, core.DefaultEmbedModel,
		strings.Join(core.ChatModels, ", "),
		strings.Join(core.EmbedModels, ", "),
		os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}
