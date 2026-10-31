package main

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
	"lmtools/internal/version"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (l *replLoop) showInfo() {
	ctx, end := l.commandContext()
	defer end()
	n := l.env.notifier
	model := core.EffectiveChatModel(l.base)
	modelLabel := model
	if l.base.Model == "" {
		modelLabel += " (default)"
	}
	n.Promptf("%s\nConfigured model: %s\n", version.String("lmc"), modelLabel)
	if served := l.env.served; served != "" && served != model {
		n.Promptf("Model reported by the provider: %s\n", served)
	}
	n.Promptf("API: %s\n", replAPIName(l.base))
	var endpoint string
	var err error
	if providers.UsesResponsesAPIWire(l.base.Provider, model, l.base.OpenAIResponses, l.base.ArgoLegacy) {
		endpoint, err = providers.ResolveResponsesURL(l.base.Provider, l.base.ProviderURL, l.base.Env)
	} else {
		endpoint, err = providers.ResolveChatURLWithArgoOptions(l.base.Provider, l.base.ProviderURL, l.base.Env, model, replStreaming(l.base), l.base.ArgoLegacy)
	}
	if err != nil {
		n.Promptf("Endpoint: unavailable (%v)\n", err)
	} else {
		// Endpoint identity does not need authentication embedded in URLs.
		if u, parseErr := url.Parse(endpoint); parseErr == nil {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			endpoint = u.String()
		}
		n.Promptf("Endpoint: %s\n", endpoint)
	}
	n.Promptf("Streaming: %s\nThinking display: %s\nToken display: %s\n", onOff(replStreaming(l.base)), onOff(l.env.cfg.ShowThinking), onOff(!l.hideUsage))
	if l.base.Effort != "" {
		n.Promptf("Reasoning effort: %s\n", l.base.Effort)
	}
	if l.base.MaxTokens > 0 {
		n.Promptf("Output token limit: %d\n", l.base.MaxTokens)
	}
	sess, err := l.currentSession(ctx)
	switch {
	case err != nil:
		n.Promptf("Session: unavailable (%v)\n", err)
	case sess == nil:
		n.Promptf("Session: not created yet\n")
	default:
		n.Promptf("Session: %s\n", session.GetSessionID(sess.Path))
		if sess.Head != nil {
			n.Promptf("Current message: %s/%s\n", session.GetSessionID(sess.Head.Path), sess.Head.ID)
		}
	}
	pending := l.pendingDescription()
	if pending == "" {
		pending = "none"
	}
	n.Promptf("Pending: %s\nLogs: %s\n", pending, l.env.logDir)
	defs := core.AdvertisedTools(l.base)
	n.Promptf("Tools: %d\n", len(defs))
	for _, def := range defs {
		name := def.Name
		if def.MCPServer != "" {
			name = def.MCPServer + "/" + def.MCPTool
		}
		n.Promptf("  %s\n", name)
	}
	if l.base.ToolEnabled {
		n.Promptf("Auto approval: %s\nNon-interactive tools: %s\nCommand timeout: %s\n", onOff(l.env.cfg.ToolAutoApprove), onOff(l.env.cfg.ToolNonInteractive), l.env.cfg.ToolTimeout)
		n.Promptf("Tool rounds per turn: %d\nParallel commands: %d\nOutput kept per command: %s\n",
			l.base.GetMaxToolRounds(), l.base.GetMaxToolParallel(), core.FormatByteCount(l.base.GetToolMaxOutputBytes()))
		if l.base.ToolWhitelist != "" {
			n.Promptf("Whitelist: %s\n", l.base.ToolWhitelist)
		}
		if l.base.ToolBlacklist != "" {
			n.Promptf("Blacklist: %s\n", l.base.ToolBlacklist)
		}
		if ui, ok := l.env.toolUI.(*replToolUI); ok {
			mode := "full"
			if ui.Options.ShortOutput {
				mode = "short (first and last lines of long output)"
			}
			n.Promptf("Tool output display: %s\n", mode)
		}
	}
	if dir, err := os.Getwd(); err == nil {
		n.Promptf("Working directory: %s\n", dir)
	}
	if l.history.file != nil {
		n.Promptf("History file: %s\n", l.history.file.path)
	}
}

func (l *replLoop) currentSession(ctx context.Context) (*session.Session, error) {
	if l.from != nil {
		return l.from, nil
	}
	if session.IsSessionResume(l.base.Resume) {
		return session.OpenSession(ctx, l.base.Resume)
	}
	return nil, nil
}

// showConversation prints the saved conversation: all of it with the system
// prompt summarized, the last turns, all of it with the system prompt, or the
// system prompt alone; or, for /show output, an output the transcript
// shortened. Thinking is shown with the thinking display on. Tool output is
// never shortened here.
func (l *replLoop) showConversation(argument string) {
	options := session.ShowOptions{SystemSummary: true, Thinking: l.env.cfg.ShowThinking, Columns: l.env.columns}
	fields := strings.Fields(argument)
	if len(fields) > 0 && fields[0] == "output" {
		ctx, end := l.commandContext()
		defer end()
		l.showOutput(ctx, strings.Join(fields[1:], " "))
		return
	}
	switch {
	case len(fields) == 0:
	case len(fields) == 1 && fields[0] == "all":
		options.SystemSummary = false
	case len(fields) == 1 && fields[0] == "system":
		options.SystemOnly = true
	case fields[0] == "last" && len(fields) <= 2:
		options.LastTurns = 1
		if len(fields) == 2 {
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 1 {
				l.env.notifier.Errorf("Use /show last N with a positive number of turns.")
				return
			}
			options.LastTurns = n
		}
	default:
		l.env.notifier.Errorf("Use /show, /show last [N], /show all, /show system, or /show output [N].")
		return
	}
	ctx, end := l.commandContext()
	defer end()
	sess, err := l.currentSession(ctx)
	if err != nil {
		l.env.notifier.Errorf("Read session: %v", err)
		return
	}
	if sess == nil {
		l.env.notifier.Infof("No saved conversation yet.")
		return
	}
	var b strings.Builder
	err = session.ShowSessionWithOptions(ctx, &b, sess, l.env.notifier, options)
	l.view(ctx, b.String())
	if err != nil {
		l.env.notifier.Errorf("Show conversation: %v", err)
	}
}

// showOutput prints the whole of an output the transcript shortened: the
// one argument numbers, or the most recent without one. It serves /show
// output at the prompt and at an approval question.
func (l *replLoop) showOutput(ctx context.Context, argument string) {
	number := 0
	if argument != "" {
		n, err := strconv.Atoi(argument)
		if err != nil || n < 1 {
			l.env.notifier.Errorf("Use /show output [N], with N from the note under an output.")
			return
		}
		number = n
	}
	output, missing := l.kept.get(number)
	if missing != "" {
		l.env.notifier.Infof("%s", missing)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Output %d, from %s:\n", output.number, output.title)
	tools.WriteOutput(ui.NewNotifierWithWriter(&b), output.text, l.env.columns)
	l.view(ctx, b.String())
}

func (l *replLoop) setDisplay(command, value string) {
	if value != "on" && value != "off" {
		l.env.notifier.Errorf("Use %s on or %s off.", command, command)
		return
	}
	enabled := value == "on"
	if command == "/thinking" {
		l.env.cfg.ShowThinking = enabled
		l.env.notifier.Infof("Thinking display %s; reasoning settings are unchanged.", value)
		l.warnHiddenThinking()
	} else {
		l.hideUsage = !enabled
		if n, ok := l.env.notifier.(interface{ SetUsageEnabled(bool) }); ok {
			n.SetUsageEnabled(enabled)
		}
		l.env.notifier.Infof("Token display %s.", value)
	}
}

// warnHiddenThinking says when the thinking display is on for a model whose
// route returns no thinking.
func (l *replLoop) warnHiddenThinking() {
	if note := argoChatHidesThinking(l.base); l.env.cfg.ShowThinking && note != "" {
		l.env.notifier.Warnf("%s.", note)
	}
}

func (l *replLoop) showHistory(argument string) {
	if argument == "clear" {
		if err := l.history.clear(); err != nil {
			l.env.notifier.Errorf("Clear history: %v", err)
			return
		}
		l.env.notifier.Infof("History cleared, including the saved history of earlier REPLs.")
		return
	}
	if argument != "" {
		id, err := strconv.Atoi(argument)
		text, ok := l.history.get(id)
		if err != nil || !ok {
			l.env.notifier.Errorf("Prompt %q is unavailable; use /history for retained prompt numbers.", argument)
			return
		}
		showTextPreview(l.env.notifier, fmt.Sprintf("Prompt %d", id), text, len(text))
		return
	}
	prompts := l.history.prompts()
	if len(prompts) == 0 {
		l.env.notifier.Infof("No prompts in the history yet.")
		return
	}
	l.env.notifier.Promptf("Prompt history (text only):\n")
	for _, entry := range prompts {
		l.env.notifier.Promptf("%d: %s\n", entry.id, plainPreview(strings.ReplaceAll(replPromptPreview(entry.text), "\n", " / ")))
	}
}

func (l *replLoop) showImages() {
	if len(l.images) == 0 {
		l.env.notifier.Infof("No pending images.")
		return
	}
	for i, image := range l.images {
		l.env.notifier.Promptf("%d: %s\n", i+1, plainPreview(core.DescribeImageBlock(image)))
	}
}

// plainPreview keeps a text's lines and spells out, on each, what a terminal
// would act on rather than show.
func plainPreview(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = core.VisibleText(line)
	}
	return strings.Join(lines, "\n")
}

// showTextPreview shows text under label, every line quoted so the preview's
// extent is plain without a closing line, and at most limit bytes of it.
func showTextPreview(n core.Notifier, label, text string, limit int) {
	end := min(len(text), limit)
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	n.Promptf("%s (%s, %d bytes):\n", label, core.Plural(1+strings.Count(text, "\n"), "line"), len(text))
	for _, line := range strings.Split(plainPreview(text[:end]), "\n") {
		if line == "" {
			n.Promptf("|\n")
		} else {
			n.Promptf("| %s\n", line)
		}
	}
	if end < len(text) {
		n.Promptf("[preview shortened; %d bytes not shown]\n", len(text)-end)
	}
}

func (l *replLoop) commandContext() (context.Context, func()) {
	if l.signals != nil {
		return l.signals.begin()
	}
	return context.WithCancel(context.Background())
}
