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
	answer       io.Writer
	diagnostics  io.Writer
	showThinking bool

	thinkingActive       bool
	thinkingHasText      bool
	thinkingEndsNewline  bool
	streamedThinking     bool
	answerWritten        bool
	answerNewlines       int
	diagnosticsSeparated bool
}

func newResponsePresenter(answer, diagnostics io.Writer, showThinking bool) *responsePresenter {
	return &responsePresenter{
		answer:       answer,
		diagnostics:  diagnostics,
		showThinking: showThinking,
	}
}

func (p *responsePresenter) HandleStreamEvent(event core.ResponseStreamEvent) {
	switch event.Type {
	case core.ResponseStreamTextDelta:
		p.finishThinking(true)
		p.writeAnswer(event.Text)
	case core.ResponseStreamReasoningStart:
		p.streamedThinking = true
		p.startThinking()
	case core.ResponseStreamReasoningDelta:
		p.streamedThinking = true
		p.writeThinking(event.Text)
	case core.ResponseStreamReasoningEnd:
		p.streamedThinking = true
		p.finishThinking(false)
	}
}

func (p *responsePresenter) HandleResponseComplete(response core.Response) {
	p.finishThinking(response.Streamed)
	presentedThinking := p.streamedThinking
	if p.showThinking && (!response.Streamed || !p.streamedThinking) {
		if response.Streamed && p.answerWritten {
			p.separateDiagnosticsFromAnswer()
		}
		presentedThinking = p.renderReasoningBlocks(response.Blocks) || presentedThinking
	}

	if !response.Streamed {
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

func (p *responsePresenter) renderReasoningBlocks(blocks []core.Block) bool {
	presented := false
	for _, candidate := range blocks {
		block, ok := candidate.(core.ReasoningBlock)
		if !ok {
			continue
		}
		text := core.ReasoningTextForDisplay(block)
		if text == "" && !shouldPresentEmptyReasoning(block) {
			continue
		}
		p.startThinking()
		p.writeThinking(text)
		p.finishThinking(false)
		presented = true
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
	if !p.answerWritten || p.diagnosticsSeparated {
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
		p.finishThinking(true)
	}
	p.thinkingActive = true
	if p.showThinking {
		_, _ = fmt.Fprintf(p.diagnostics, "%s\n", thinkingStartMarker)
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
		if incomplete {
			marker = thinkingIncompleteEndMarker
		}
		_, _ = fmt.Fprintf(p.diagnostics, "%s\n\n", marker)
	}
	p.thinkingActive = false
	p.thinkingHasText = false
	p.thinkingEndsNewline = false
}

func (p *responsePresenter) Close() {
	p.finishThinking(true)
	p.separateDiagnosticsFromAnswer()
}
