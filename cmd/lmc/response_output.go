package main

import (
	"fmt"
	"io"
	"lmtools/internal/core"
	"strings"
)

const (
	thinkingStartMarker         = "--- thinking ---"
	thinkingEndMarker           = "--- end thinking ---"
	thinkingIncompleteEndMarker = "--- end thinking (incomplete) ---"
	omittedThinkingText         = "[omitted by provider]"
)

// presenterProgress hears what a response is doing as it streams, so a status
// line can explain a silence; the REPL's turnProgress implements it.
type presenterProgress interface {
	streamEvent(core.ResponseStreamEvent)
	responseComplete()
}

// responsePresenter keeps the assistant answer on stdout while optionally
// rendering provider-visible reasoning summaries on stderr. A single instance
// is reused for the initial response and every tool follow-up round.
type responsePresenter struct {
	answer         io.Writer
	diagnostics    io.Writer
	showThinking   bool
	answerLabel    string
	progress       presenterProgress
	lastIncomplete bool
	// separate keeps a note off the end of an answer on the screen both
	// share; newResponsePresenter sets it.
	separate bool
	// notifier carries the presenter's own note, in the run's style; a
	// presenter without one writes no note.
	notifier core.Notifier
	thinking thinkingText

	thinkingActive       bool
	thinkingEnded        bool
	thinkingIncomplete   bool
	streamedThinking     bool
	answerWritten        bool
	answerLabelNeeded    bool
	answerNewlines       int
	diagnosticsSeparated bool
}

func newResponsePresenter(answer, diagnostics io.Writer, showThinking bool) *responsePresenter {
	p := &responsePresenter{
		answer:       answer,
		diagnostics:  diagnostics,
		showThinking: showThinking,
		separate:     true,
	}
	p.thinking.out = diagnostics
	p.thinking.reset()
	return p
}

// quoteThinking formats visible thinking for a person at a terminal: each
// line quoted, words wrapped at width, control characters escaped. A nil
// width quotes without wrapping.
func (p *responsePresenter) quoteThinking(width func() int) {
	p.thinking.quote = thinkingQuote
	p.thinking.width = width
	p.thinking.escape = true
}

func (p *responsePresenter) HandleStreamEvent(event core.ResponseStreamEvent) {
	// Hidden thinking writes nothing, so its deltas are no sign of output:
	// the status keeps counting the thinking from its start.
	if p.progress != nil && (event.Type != core.ResponseStreamReasoningDelta || p.showThinking) {
		p.progress.streamEvent(event)
	}
	switch event.Type {
	case core.ResponseStreamTextDelta:
		p.finishThinking(!p.thinkingEnded)
		p.writeAnswer(event.Text)
	case core.ResponseStreamToolCallStart:
		// The arguments arrive silently; end the open section and the
		// answer line so the status explaining the wait has a line of its own.
		p.finishThinking(!p.thinkingEnded)
		p.separateDiagnosticsFromAnswer()
	case core.ResponseStreamReasoningStart:
		p.streamedThinking = true
		p.startThinking()
	case core.ResponseStreamReasoningDelta:
		p.streamedThinking = true
		if p.thinkingEnded {
			p.finishThinking(false)
		}
		p.writeThinking(event.Text)
	case core.ResponseStreamReasoningEnd:
		p.streamedThinking = true
		// The terminal response can still report a token limit. Defer the
		// marker until the next block/answer or completion knows the outcome;
		// the last word need not wait for it.
		if p.thinkingActive {
			p.thinkingEnded = true
			p.thinkingIncomplete = event.Incomplete
			p.thinking.flushWord()
		}
	}
}

func (p *responsePresenter) HandleResponseComplete(response core.Response) {
	p.lastIncomplete = response.IncompleteReason != ""
	if p.progress != nil {
		p.progress.responseComplete()
	}
	p.finishThinking((response.Streamed && !p.thinkingEnded) || response.IncompleteReason != "")
	presentedThinking := p.streamedThinking
	answerInBlocks := p.showThinking && !response.Streamed && answerBlocksMatch(response)
	if p.showThinking && (!response.Streamed || !p.streamedThinking) {
		if response.Streamed && p.answerWritten {
			p.separateDiagnosticsFromAnswer()
		}
		presentedThinking = p.renderResponseBlocks(response, answerInBlocks) || presentedThinking
	}

	if !response.Streamed && !answerInBlocks {
		p.writeAnswer(response.Text)
	}

	// Adaptive models often answer without thinking at all; only reasoning
	// the provider counted and did not show is worth a note.
	if reasoning := reportedReasoningTokens(response.Usage); p.notifier != nil && p.showThinking && !presentedThinking && reasoning > 0 {
		p.separateDiagnosticsFromAnswer()
		p.notifier.Infof("No visible thinking was returned for %d reasoning tokens.", reasoning)
	}

	if response.Usage != nil || len(response.ToolCalls) > 0 {
		p.separateDiagnosticsFromAnswer()
	}
	p.resetResponseState()
}

// Ordered blocks can preserve buffered text/thinking transitions only if their
// text reproduces the answer exactly. Legacy projections still use Text.
func answerBlocksMatch(response core.Response) bool {
	text := response.Text
	for _, block := range response.Blocks {
		if block, ok := block.(core.TextBlock); ok {
			if !strings.HasPrefix(text, block.Text) {
				return false
			}
			text = text[len(block.Text):]
		}
	}
	return len(response.Blocks) > 0 && text == ""
}

func (p *responsePresenter) renderResponseBlocks(response core.Response, includeAnswer bool) bool {
	presented := false
	incomplete := response.IncompleteReason != ""
	var previous core.Block
	for _, candidate := range response.Blocks {
		block, isReasoning := candidate.(core.ReasoningBlock)
		if !isReasoning {
			p.finishThinking(incomplete)
			if text, ok := candidate.(core.TextBlock); ok && includeAnswer {
				p.writeAnswer(text.Text)
			}
			previous = candidate
			continue
		}
		text := core.ReasoningTextForDisplay(block)
		if text == "" && !shouldPresentEmptyReasoning(block) {
			continue
		}
		if !p.thinkingActive || !core.ContinuesReasoningSection(previous, block) {
			p.finishThinking(incomplete)
			p.startThinking()
		} else if text != "" && p.thinking.wrote {
			p.writeThinking("\n")
		}
		p.writeThinking(text)
		presented = true
		previous = candidate
	}
	p.finishThinking(incomplete)
	return presented
}

func shouldPresentEmptyReasoning(block core.ReasoningBlock) bool {
	switch block.Type {
	case "thinking", "redacted_thinking", "reasoning":
		return true
	default:
		return false
	}
}

func reportedReasoningTokens(usage *core.Usage) int {
	if usage == nil || usage.ReasoningTokens == nil {
		return 0
	}
	return *usage.ReasoningTokens
}

func (p *responsePresenter) writeThinking(text string) {
	if text == "" {
		return
	}
	if !p.thinkingActive {
		p.startThinking()
	}
	if !p.showThinking {
		return
	}
	p.thinking.write(text)
}

func (p *responsePresenter) writeAnswer(text string) {
	if text == "" {
		return
	}
	if (!p.answerWritten || p.answerLabelNeeded) && p.answerLabel != "" {
		_, _ = fmt.Fprintf(p.diagnostics, "%s:\n", p.answerLabel)
	}
	p.answerLabelNeeded = false
	_, _ = fmt.Fprint(p.answer, text)
	p.answerWritten = true
	p.diagnosticsSeparated = false
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			if p.answerNewlines < 2 {
				p.answerNewlines++
			}
			continue
		}
		p.answerNewlines = 0
	}
}

// separateDiagnosticsFromAnswer preserves stdout byte-for-byte while ensuring
// that a note, tool transcript, or error written to stderr cannot be glued to
// the assistant's last streamed byte in an interactive terminal.
func (p *responsePresenter) separateDiagnosticsFromAnswer() {
	if !p.separate || !p.answerWritten || p.diagnosticsSeparated {
		return
	}
	if missing := 2 - p.answerNewlines; missing > 0 {
		_, _ = fmt.Fprint(p.diagnostics, strings.Repeat("\n", missing))
	}
	p.diagnosticsSeparated = true
}

func (p *responsePresenter) resetResponseState() {
	p.streamedThinking = false
	p.answerWritten = false
	p.answerNewlines = 0
	p.diagnosticsSeparated = false
}

func (p *responsePresenter) startThinking() {
	if p.thinkingActive {
		p.finishThinking(!p.thinkingEnded)
	}
	p.thinkingActive = true
	p.thinkingEnded = false
	p.thinkingIncomplete = false
	p.thinking.reset()
	if p.showThinking {
		p.separateDiagnosticsFromAnswer()
		_, _ = fmt.Fprintf(p.diagnostics, "%s\n", thinkingStartMarker)
		p.answerLabelNeeded = true
	}
}

func (p *responsePresenter) finishThinking(incomplete bool) {
	if !p.thinkingActive {
		return
	}
	if p.showThinking {
		if !p.thinking.wrote {
			p.thinking.write(omittedThinkingText)
		}
		p.thinking.finish()
		marker := thinkingEndMarker
		if incomplete || p.thinkingIncomplete {
			marker = thinkingIncompleteEndMarker
		}
		_, _ = fmt.Fprintf(p.diagnostics, "%s\n\n", marker)
	}
	p.thinkingActive = false
	p.thinkingEnded = false
	p.thinkingIncomplete = false
	p.thinking.reset()
}

func (p *responsePresenter) Close() {
	p.finishThinking(!p.thinkingEnded)
	p.separateDiagnosticsFromAnswer()
}
