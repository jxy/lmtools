package ui

import (
	"bytes"
	"lmtools/internal/core"
	"testing"
)

func TestREPLUsageNumbersResponsesIncludingMissingOrHiddenUsage(t *testing.T) {
	var out bytes.Buffer
	n := NewREPLNotifier(&out)
	u := core.UsageFromPayload([]byte(`{"usage":{"input_tokens":5,"output_tokens":2}}`))
	n.BeginTurn()
	n.ReportUsage(nil, false)
	n.ReportUsage(u, false)
	n.SetUsageEnabled(false)
	n.ReportUsage(u, false)
	n.SetUsageEnabled(true)
	n.ReportUsage(u, false)
	n.BeginTurn()
	n.ReportUsage(u, false)
	want := "Tokens (response 2): input 5 | output 2\nTokens (response 4): input 5 | output 2\nTokens: input 5 | output 2\n"
	if out.String() != want {
		t.Fatalf("usage=%q", out.String())
	}
}

// A response that asks for tools is numbered from the first, and its line
// prints when the response ends, ahead of the calls it asks for.
func TestREPLUsagePrintsAtOnceForAResponseWithToolCalls(t *testing.T) {
	var out bytes.Buffer
	n := NewREPLNotifier(&out)
	n.BeginTurn()
	u := core.UsageFromPayload([]byte(`{"usage":{"input_tokens":5,"output_tokens":2}}`))
	n.ReportUsage(u, true)
	n.Promptf("Tools: 1\n")
	if want := "Tokens (response 1): input 5 | output 2\nTools: 1\n"; out.String() != want {
		t.Fatalf("transcript = %q, want %q", out.String(), want)
	}
}
