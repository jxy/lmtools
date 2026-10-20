package main

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"lmtools/internal/session"
	"lmtools/internal/version"
	"net/url"
	"strconv"
	"strings"
	"unicode"
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
	n.Promptf("%s\nConfigured model: %s\nAPI: %s\n", version.String("lmc"), modelLabel, replAPIName(l.base))
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

func (l *replLoop) showConversation() {
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
	if err := session.ShowSessionTo(ctx, l.diag, sess, l.env.notifier); err != nil {
		l.env.notifier.Errorf("Show conversation: %v", err)
	}
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
	} else {
		l.hideUsage = !enabled
		if n, ok := l.env.notifier.(interface{ SetUsageEnabled(bool) }); ok {
			n.SetUsageEnabled(enabled)
		}
		l.env.notifier.Infof("Token display %s.", value)
	}
}

func (l *replLoop) showHistory(argument string) {
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
	if len(l.history.entries) == 0 {
		l.env.notifier.Infof("No prompt text in this invocation's history yet.")
		return
	}
	l.env.notifier.Promptf("Prompt history (this invocation; text only):\n")
	for _, entry := range l.history.entries {
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

// plainPreview preserves readable lines while making terminal control bytes visible.
func plainPreview(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func showTextPreview(n core.Notifier, label, text string, limit int) {
	end := min(len(text), limit)
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	n.Promptf("%s (%d bytes):\n", label, len(text))
	for _, line := range strings.Split(plainPreview(text[:end]), "\n") {
		n.Promptf("  %s\n", line)
	}
	if end < len(text) {
		n.Promptf("  [preview shortened; %d bytes omitted]\n", len(text)-end)
	}
	n.Promptf("End %s.\n", strings.ToLower(label))
}

func (l *replLoop) commandContext() (context.Context, func()) {
	if l.signals != nil {
		return l.signals.begin()
	}
	return context.WithCancel(context.Background())
}
