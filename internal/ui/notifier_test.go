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
	n.ReportUsage(nil)
	n.ReportUsage(u)
	n.SetUsageEnabled(false)
	n.ReportUsage(u)
	n.SetUsageEnabled(true)
	n.ReportUsage(u)
	n.BeginTurn()
	n.ReportUsage(u)
	want := "Tokens (response 2): input 5 | output 2\nTokens (response 4): input 5 | output 2\nTokens: input 5 | output 2\n"
	if out.String() != want {
		t.Fatalf("usage=%q", out.String())
	}
}
