package main

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"lmtools/internal/session"
	"strings"
)

func (l *replLoop) showStartup() {
	if !l.terminal {
		return
	}
	mode := "buffered"
	if replStreaming(l.base) {
		mode = "streaming"
	}
	l.env.notifier.Promptf("lmc | model: %s | %s | thinking %s\n", core.EffectiveChatModel(l.base), mode, onOff(l.env.cfg.ShowThinking))
	l.env.notifier.Promptf("/help commands | /info settings | Ctrl-C cancel | Ctrl-D exit\n")
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
	case in.isRegeneration:
		l.env.notifier.Promptf("Regenerating the selected answer.\n")
	case in.text == "" && len(images) == 0:
		l.env.notifier.Promptf("Recovering pending tools.\n")
	default:
		if !l.input.editing {
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

func (l *replLoop) showSession(sess *session.Session) {
	if !l.terminal || sess == nil {
		return
	}
	id := session.GetSessionID(sess.Path)
	if id == "" || id == l.shownSession {
		return
	}
	l.separateAnswer()
	l.env.notifier.Infof("Session: %s", id)
	l.shownSession = id
}
