package main

import (
	"io"
	"lmtools/internal/core"
	"lmtools/internal/ui/tools"
	"slices"
	"sync"
)

// replToolUI is the tool transcript of a REPL on a terminal. It adds what a
// person watching needs to the shared renderer: a status line naming the
// calls still running, which drops each call as it ends, and the ^C of an
// interrupt before the results that follow it.
type replToolUI struct {
	*tools.CLIToolUI
	loop *replLoop

	mu      sync.Mutex
	running map[int]string
}

func (u *replToolUI) CallStarted(index int, call core.ToolCall) {
	u.mu.Lock()
	if u.running == nil {
		u.running = make(map[int]string)
	}
	first := len(u.running) == 0
	u.running[index] = core.ToolCallDisplayName(call, nil)
	names := u.runningNamesLocked()
	u.mu.Unlock()
	if progress := u.loop.env.progress; progress != nil {
		if first {
			progress.running(names)
		} else {
			progress.stillRunning(names)
		}
	}
}

// CallFinished drops the call from the status; its result may wait for
// slower calls requested before it.
func (u *replToolUI) CallFinished(index int) {
	u.mu.Lock()
	delete(u.running, index)
	names := u.runningNamesLocked()
	u.mu.Unlock()
	if progress := u.loop.env.progress; progress != nil {
		progress.stillRunning(names)
	}
}

func (u *replToolUI) ResultReady(index, total int, call core.ToolCall, result core.ToolResult) {
	u.loop.echoInterrupt()
	u.CLIToolUI.ResultReady(index, total, call, result)
}

func (u *replToolUI) AfterExecute(calls []core.ToolCall, results []core.ToolResult) {
	u.mu.Lock()
	clear(u.running)
	u.mu.Unlock()
	if progress := u.loop.env.progress; progress != nil {
		progress.stop()
	}
	u.loop.echoInterrupt()
	u.CLIToolUI.AfterExecute(calls, results)
}

func (u *replToolUI) runningNamesLocked() []string {
	indexes := make([]int, 0, len(u.running))
	for index := range u.running {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	names := make([]string, len(indexes))
	for i, index := range indexes {
		names[i] = u.running[index]
	}
	return names
}

// squeezedWriter writes diagnostics to the screen they share with answers
// without opening a second empty line in a row: the tool transcript starts
// its blocks with an empty line, which after an answer the presenter has
// already separated would show as two.
type squeezedWriter struct {
	w      io.Writer
	screen *replScreen
}

func (s squeezedWriter) Write(p []byte) (int, error) {
	allowed := max(0, 2-s.screen.trailingNewlines())
	leading := 0
	for leading < len(p) && p[leading] == '\n' {
		leading++
	}
	skip := max(0, leading-allowed)
	if skip == len(p) {
		return len(p), nil
	}
	n, err := s.w.Write(p[skip:])
	return n + skip, err
}

// trailingNewlines reports how many newlines what the screen showed last ends
// with, up to two; a screen that showed nothing counts as an empty line.
func (s *replScreen) trailingNewlines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wrote {
		return 2
	}
	return s.newlines
}
