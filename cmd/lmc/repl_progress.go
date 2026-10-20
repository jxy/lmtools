package main

import (
	"fmt"
	"io"
	"lmtools/internal/retry"
	"sync"
	"time"
)

const waitingStatusDelay = 500 * time.Millisecond

// turnProgress has no input reader and emits no animation. Stopping serializes
// with the delayed callback so no stale status can appear after an answer.
type turnProgress struct {
	mu     sync.Mutex
	timer  *time.Timer
	epoch  uint64
	screen *replScreen
}

func (p *turnProgress) report(event retry.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.epoch++
	if p.timer != nil {
		p.timer.Stop()
	}
	if event.Delay > 0 {
		p.screen.setStatus(fmt.Sprintf("Retrying in %s (attempt %d/%d)...", event.Delay.Round(time.Millisecond), event.Attempt, event.Total))
		return
	}
	p.screen.setStatus("")
	epoch := p.epoch
	p.timer = time.AfterFunc(waitingStatusDelay, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.epoch == epoch {
			p.screen.setStatus("Waiting for response...")
		}
	})
}

func (p *turnProgress) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.epoch++
	if p.timer != nil {
		p.timer.Stop()
	}
	p.screen.setStatus("")
}

func (s *replScreen) setStatus(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusOut == nil {
		return
	}
	if !s.inlineStatus {
		if text != "" {
			_, _ = fmt.Fprintln(s.statusOut, text)
			s.answerLast, s.newlines = false, 1
		}
		return
	}
	s.clearStatusLocked()
	columns := 80
	if s.columns != nil {
		columns = s.columns()
	}
	text = text[:min(len(text), max(0, columns-1))]
	if text != "" {
		_, _ = io.WriteString(s.statusOut, text)
		s.statusLine = terminalLine{text: text, cursor: len(text), columns: columns}
	}
}

func (s *replScreen) clearStatusLocked() {
	if s.statusLine.text != "" {
		columns := s.statusLine.columns
		if s.columns != nil {
			columns = s.columns()
		}
		s.statusLine.clear(s.statusOut, columns)
	}
}
