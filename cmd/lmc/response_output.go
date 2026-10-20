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
	noThinkingSummaryNote       = "Note: No visible thinking summary was returned for this response.\n"
)

// responsePresenter keeps the assistant answer on stdout while optionally
// rendering provider-visible reasoning summaries on stderr. A single instance
// is reused for the initial response and every tool follow-up round.
type responsePresenter struct {
	answer         io.Writer
	diagnostics    io.Writer
	showThinking   bool
	answerLabel    string
	onOutput       func()
	lastIncomplete bool
	// separate keeps a note off the end of an answer on the screen both
	// share; newResponsePresenter sets it.
	separate bool

	thinkingActive       bool
	thinkingEnded        bool
	thinkingIncomplete   bool
	thinkingHasText      bool
	thinkingEndsNewline  bool
	streamedThinking     bool
	answerWritten        bool
	answerLabelNeeded    bool
	answerNewlines       int
	diagnosticsSeparated bool
}

func newResponsePresenter(answer, diagnostics io.Writer, showThinking bool) *responsePresenter {
	return &responsePresenter{
		answer:       answer,
		diagnostics:  diagnostics,
		showThinking: showThinking,
		separate:     true,
	}
}

func (p *responsePresenter) HandleStreamEvent(event core.ResponseStreamEvent) {
	if p.onOutput != nil && (event.Type == core.ResponseStreamTextDelta || p.showThinking) {
		p.onOutput()
	}
	switch event.Type {
	case core.ResponseStreamTextDelta:
		p.finishThinking(!p.thinkingEnded)
		p.writeAnswer(event.Text)
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
		// marker until the next block/answer or completion knows the outcome.
		if p.thinkingActive {
			p.thinkingEnded = true
			p.thinkingIncomplete = event.Incomplete
		}
	}
}

func (p *responsePresenter) HandleResponseComplete(response core.Response) {
	p.lastIncomplete = response.IncompleteReason != ""
	if p.onOutput != nil {
		p.onOutput()
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

	if p.showThinking && !presentedThinking {
		p.separateDiagnosticsFromAnswer()
		_, _ = fmt.Fprint(p.diagnostics, noThinkingSummaryNote)
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
	for _, candidate := range response.Blocks {
		switch block := candidate.(type) {
		case core.TextBlock:
			if includeAnswer {
				p.writeAnswer(block.Text)
			}
		case core.ReasoningBlock:
			text := core.ReasoningTextForDisplay(block)
			if text == "" && !shouldPresentEmptyReasoning(block) {
				continue
			}
			p.startThinking()
			p.writeThinking(text)
			p.finishThinking(response.IncompleteReason != "")
			presented = true
		}
	}
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
	_, _ = fmt.Fprint(p.diagnostics, text)
	p.thinkingHasText = true
	p.thinkingEndsNewline = text[len(text)-1] == '\n'
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
		if !p.thinkingHasText {
			_, _ = fmt.Fprintf(p.diagnostics, "%s\n", omittedThinkingText)
		} else if !p.thinkingEndsNewline {
			_, _ = fmt.Fprint(p.diagnostics, "\n")
		}
		marker := thinkingEndMarker
		if incomplete || p.thinkingIncomplete {
			marker = thinkingIncompleteEndMarker
		}
		_, _ = fmt.Fprintf(p.diagnostics, "%s\n\n", marker)
	}
	p.thinkingActive = false
	p.thinkingEnded = false
	p.thinkingIncomplete = false
	p.thinkingHasText = false
	p.thinkingEndsNewline = false
}

func (p *responsePresenter) Close() {
	p.finishThinking(!p.thinkingEnded)
	p.separateDiagnosticsFromAnswer()
}
