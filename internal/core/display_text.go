package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// IsHiddenControl reports a character a terminal acts on rather than shows:
// a control other than tab (C0, DEL, and C1), or a format character such as
// a bidi override, which reorders what is displayed.
func IsHiddenControl(r rune) bool {
	return r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r))
}

// VisibleText spells every hidden character of text as \uXXXX, so text a
// model or a command produced cannot move the cursor, recolor the terminal,
// or reorder what the operator reads.
func VisibleText(text string) string {
	if strings.IndexFunc(text, IsHiddenControl) < 0 {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		if IsHiddenControl(r) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DisplayName renders a name for one line of the transcript. A name that is
// empty, or holds a space, quote, backslash, or hidden character, is quoted
// with its hidden characters spelled out, so where it ends and what it holds
// cannot be misread.
func DisplayName(name string) string {
	plain := name != "" && strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || r == '"' || r == '\\' || IsHiddenControl(r)
	}) < 0
	if plain {
		return name
	}
	return MarshalJSONForDisplay(name)
}

// ToolCallDisplayName names a call on one line for the operator: the program
// of a command, server/tool for an MCP call, the tool name otherwise, through
// DisplayName. args, when the caller has parsed the call's arguments, saves
// parsing them again.
func ToolCallDisplayName(call ToolCall, args *UniversalCommandArgs) string {
	name := call.Name
	switch {
	case call.MCPServer != "":
		name = call.MCPServer + "/" + call.MCPTool
	case call.Name == UniversalCommandToolName:
		if args == nil {
			var parsed UniversalCommandArgs
			if json.Unmarshal(call.Args, &parsed) == nil {
				args = &parsed
			}
		}
		if args != nil && len(args.Command) > 0 {
			name = args.Command[0]
		}
	}
	return DisplayName(name)
}

// Plural counts n of noun, adding s for any count but one.
func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
