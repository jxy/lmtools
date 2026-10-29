package main

import (
	"fmt"
	"sync"
)

// maxKeptOutputBytes bounds the shortened output the REPL keeps for /show
// output: any number of outputs, the oldest dropped once their text passes
// this total. The newest is always kept, however large.
const maxKeptOutputBytes = 64 << 20

// keptOutputs holds the whole text of each tool output the transcript
// shortened, numbered from 1 for the REPL's life, so the note under the
// output can name the command that prints it. Numbers are never reused.
type keptOutputs struct {
	mu      sync.Mutex
	outputs []keptOutput
	bytes   int
	last    int
	// limit replaces maxKeptOutputBytes when set.
	limit int
}

type keptOutput struct {
	number int
	title  string
	text   string
}

// keep stores text under the next number and returns the command that
// prints it.
func (k *keptOutputs) keep(title, text string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.last++
	k.outputs = append(k.outputs, keptOutput{number: k.last, title: title, text: text})
	k.bytes += len(text)
	limit := k.limit
	if limit == 0 {
		limit = maxKeptOutputBytes
	}
	for len(k.outputs) > 1 && k.bytes > limit {
		k.bytes -= len(k.outputs[0].text)
		k.outputs[0] = keptOutput{}
		k.outputs = k.outputs[1:]
	}
	return fmt.Sprintf("/show output %d", k.last)
}

// get returns output number, or the most recent for number 0. When there is
// none to return, it says why, in words for the operator.
func (k *keptOutputs) get(number int) (keptOutput, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case k.last == 0:
		return keptOutput{}, "No tool output has been shortened yet."
	case number == 0:
		return k.outputs[len(k.outputs)-1], ""
	case number > k.last:
		return keptOutput{}, fmt.Sprintf("There is no output %d; the latest is %d.", number, k.last)
	case number < k.outputs[0].number:
		return keptOutput{}, fmt.Sprintf("Output %d is no longer kept (64 MiB limit); /show last N prints saved turns.", number)
	}
	return k.outputs[number-k.outputs[0].number], ""
}
