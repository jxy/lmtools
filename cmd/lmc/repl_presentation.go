package main

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"lmtools/internal/session"
	"os"
	"strings"
)

// showStartup names what the conversation will use: the model and effort,
// how answers arrive, whether thinking is shown, and the tools with the
// directory their commands run in.
func (l *replLoop) showStartup() {
	if !l.terminal {
		return
	}
	n := l.env.notifier
	n.Promptf("%s\n", l.settingsLine())
	if tools := l.toolSummary(); tools != "" {
		n.Promptf("%s\n", tools)
	}
	n.Promptf("/help commands | /info settings | Ctrl-C cancel | Ctrl-D exit\n")
}

func (l *replLoop) settingsLine() string {
	mode := "buffered"
	if replStreaming(l.base) {
		mode = "streaming"
	}
	parts := []string{"lmc", "model: " + core.EffectiveChatModel(l.base)}
	if l.base.Effort != "" {
		parts = append(parts, "effort: "+l.base.Effort)
	}
	parts = append(parts, mode, "thinking display: "+onOff(l.env.cfg.ShowThinking))
	return strings.Join(parts, " | ")
}

// toolSummary names the advertised tools, a server's tools as a count, and
// where commands run; it is empty when tools are off.
func (l *replLoop) toolSummary() string {
	if !l.base.ToolEnabled {
		return ""
	}
	var names []string
	servers := map[string]int{}
	var order []string
	for _, def := range core.AdvertisedTools(l.base) {
		if def.MCPServer == "" {
			names = append(names, def.Name)
			continue
		}
		if servers[def.MCPServer] == 0 {
			order = append(order, def.MCPServer)
		}
		servers[def.MCPServer]++
	}
	for _, server := range order {
		names = append(names, fmt.Sprintf("%s (%d MCP tools)", server, servers[server]))
	}
	line := "tools: " + strings.Join(names, ", ")
	if dir, err := os.Getwd(); err == nil {
		line += " | commands run in " + dir
	}
	return line
}

func replAPIName(opts core.RequestOptions) string {
	opts.Model = core.EffectiveChatModel(opts)
	if providers.UsesResponsesAPIWire(opts.Provider, opts.Model, opts.OpenAIResponses, opts.ArgoLegacy) {
		return "Responses"
	}
	provider := opts.Provider
	if provider == constants.ProviderArgo {
		if opts.ArgoLegacy {
			return "Argo chat"
		}
		provider = providers.DetermineArgoModelProvider(opts.Model)
	}
	switch provider {
	case constants.ProviderAnthropic:
		return "Messages"
	case constants.ProviderGoogle:
		return "GenerateContent"
	default:
		return "Chat Completions"
	}
}

func (l *replLoop) separateAnswer() {
	if l.screen != nil {
		l.screen.separateAnswer(l.diag)
	}
}

func replStreaming(opts core.RequestOptions) bool {
	return opts.StreamChat && (!opts.ArgoLegacy || !opts.ToolEnabled)
}

// showTurn identifies non-text submissions and sets the answer heading. With
// application-owned echo, ordinary input is already visible above the response.
func (l *replLoop) showTurn(in turnInput, images []core.ImageBlock) {
	l.env.answerLabel = ""
	if !l.terminal {
		return
	}
	if l.env.answersOnScreen {
		l.env.answerLabel = "Assistant"
	}
	l.separateAnswer()
	switch {
	case in.text == "" && len(images) == 0:
		l.env.notifier.Promptf("Asking for the answer the conversation awaits.\n")
	default:
		// Without the editor the terminal echoed each line as typed; a turn
		// of several lines is shown once more as one.
		if !l.input.editing && strings.Contains(in.text, "\n") {
			showTextPreview(l.env.notifier, "Submitted prompt", in.text, maxEditorPreviewBytes)
		}
		if len(images) > 0 {
			l.env.notifier.Promptf("Sending %d image(s).\n", len(images))
		}
	}
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func (l *replLoop) pendingDescription() string {
	var parts []string
	if l.draft != "" {
		parts = append(parts, "draft")
	}
	if len(l.images) > 0 {
		parts = append(parts, fmt.Sprintf("%d image(s)", len(l.images)))
	}
	if l.base.Developer != "" {
		parts = append(parts, "developer instruction")
	}
	return strings.Join(parts, ", ")
}

func (l *replLoop) reportUnsent() {
	if pending := l.pendingDescription(); pending != "" {
		l.env.notifier.Infof("Not sent: %s.", pending)
	}
}

// leave reports, as the loop ends, what was left unsent and, when the loop
// saved anything, where, with the flag that continues the conversation.
func (l *replLoop) leave() {
	l.reportUnsent()
	if !l.saved || l.from == nil || l.from.Head == "" {
		return
	}
	l.env.notifier.Infof("Saved in session %s at message %s; continue it with -resume %s.", l.from.Number, l.from.Head, l.from.Ref())
}

// showSession names the session a terminal loop continues, once.
func (l *replLoop) showSession(sess *session.Session) {
	if !l.terminal || sess == nil || sess.Number == l.shownSession {
		return
	}
	l.separateAnswer()
	l.env.notifier.Infof("Session: %s", sess.Number)
	l.shownSession = sess.Number
}
