package main

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"lmtools/internal/session"
)

func (l *replLoop) showStartup() {
	if !l.terminal {
		return
	}
	mode := "buffered"
	if l.base.StreamChat {
		mode = "streaming"
	}
	l.env.notifier.Promptf("lmc REPL | model: %q | API: %s (%s)\n", l.base.Model, replAPIName(l.base), mode)
	l.env.notifier.Promptf("End a line with \\ to continue; use \\\\ for a literal trailing \\.\n")
	l.env.notifier.Promptf("/help for commands; Ctrl-C cancels, Ctrl-D or /exit exits.\n")
}

func replAPIName(opts core.RequestOptions) string {
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

// showTurn identifies the submitted text even when several pasted lines were
// echoed before the first answer. A plain status line signals activity without
// a background writer competing with answer, thinking, or approval output.
func (l *replLoop) showTurn(in turnInput, images []core.ImageBlock) {
	l.env.answerLabel = ""
	if !l.terminal {
		return
	}
	l.turn++
	if l.env.answersOnScreen {
		l.env.answerLabel = fmt.Sprintf("Assistant %d", l.turn)
	}
	l.separateAnswer()
	switch {
	case in.isRegeneration:
		l.env.notifier.Promptf("Turn %d: Regenerating the selected answer.\n", l.turn)
	case in.text == "" && len(images) == 0:
		l.env.notifier.Promptf("Turn %d: Recovering pending tools.\n", l.turn)
	default:
		l.env.notifier.Promptf("User %d: %q", l.turn, replPromptPreview(in.text))
		if len(images) > 0 {
			l.env.notifier.Promptf(" (%d image(s))", len(images))
		}
		l.env.notifier.Promptf("\n")
	}
	l.env.notifier.Promptf("lmc: Working on turn %d... (Ctrl-C to cancel)\n", l.turn)
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
