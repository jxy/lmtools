package core

import (
	"encoding/json"
	"testing"
)

func TestVisibleTextSpellsOutHiddenCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":        "plain text",
		"tab\tkept":         "tab\tkept",
		"\x1b]0;title\x07":  `\u001b]0;title\u0007`,
		"del\x7f":           `del\u007f`,
		"c1\u009bcsi":       `c1\u009bcsi`,
		"bidi\u202eevil":    `bidi\u202eevil`,
		"line\nbreak\rhere": `line\u000abreak\u000dhere`,
	} {
		if got := VisibleText(in); got != want {
			t.Errorf("VisibleText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayNameQuotesWhatCouldBeMisread(t *testing.T) {
	for in, want := range map[string]string{
		"ls":            "ls",
		"server/tool":   "server/tool",
		"":              `""`,
		"my prog":       `"my prog"`,
		`say"hi`:        `"say\"hi"`,
		`back\slash`:    `"back\\slash"`,
		"\x1b[31mls":    `"\u001b[31mls"`,
		"evil\u202ecod": `"evil\u202ecod"`,
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Display JSON spells out what encoding/json leaves raw and still decodes to
// the same value, so a suggested rule copied from a hint still matches.
func TestMarshalJSONForDisplayEscapesAndStaysValid(t *testing.T) {
	argv := []string{"echo", "a\u009bb", "c\u202ed", "e\x7ff"}
	got := MarshalJSONForDisplay(argv)
	if want := `["echo","a\u009bb","c\u202ed","e\u007ff"]`; got != want {
		t.Fatalf("MarshalJSONForDisplay = %s, want %s", got, want)
	}
	var back []string
	if err := json.Unmarshal([]byte(got), &back); err != nil || len(back) != len(argv) || back[1] != argv[1] || back[2] != argv[2] || back[3] != argv[3] {
		t.Fatalf("round trip = %q, %v", back, err)
	}
}

func TestToolCallDisplayNameUsesParsedArguments(t *testing.T) {
	call := ToolCall{Name: UniversalCommandToolName, Args: json.RawMessage(`{"command":["ls","-l"]}`)}
	if got := ToolCallDisplayName(call, nil); got != "ls" {
		t.Fatalf("parsed here = %q", got)
	}
	if got := ToolCallDisplayName(call, &UniversalCommandArgs{Command: []string{"grep"}}); got != "grep" {
		t.Fatalf("parsed by the caller = %q", got)
	}
	mcp := ToolCall{Name: "mcp__fs__read", MCPServer: "fs", MCPTool: "read file"}
	if got := ToolCallDisplayName(mcp, nil); got != `"fs/read file"` {
		t.Fatalf("MCP call = %q", got)
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "0 lines", 1: "1 line", 2: "2 lines"} {
		if got := Plural(n, "line"); got != want {
			t.Errorf("Plural(%d) = %q, want %q", n, got, want)
		}
	}
}
