package session

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
	"strings"
)

// ShowOptions narrow and annotate a conversation display.
type ShowOptions struct {
	// LastTurns, when positive, shows only that many of the latest turns. A
	// turn starts at a user message other than a round of tool results.
	LastTurns int
	// SystemSummary shows a long custom system prompt as one line saying
	// how long it is; SystemOnly shows nothing but the system prompt.
	SystemSummary bool
	SystemOnly    bool
	// Thinking shows the reasoning text saved with each answer.
	Thinking bool
	// Columns, when set, is the terminal's width, within which tool rounds
	// wrap their lines as they do live.
	Columns func() int
}

// FormatRole formats role with optional model.
func FormatRole(role, model string) string {
	if role == string(core.RoleAssistant) && model != "" {
		return role + "/" + model
	}
	return role
}

// Show prints the history ending at the message at names. Each message is
// labeled with its ID and its parent's, since IDs from other branches can
// fall between a parent and a child, and a message with alternatives lists
// their IDs.
func (s *Store) Show(ctx context.Context, w io.Writer, at Ref, options ShowOptions) error {
	h, err := s.History(at)
	if err != nil {
		return err
	}
	if options.SystemOnly {
		showSystemOnly(w, at, SettingsAt(h.Messages))
		return nil
	}
	if len(h.Messages) == 0 {
		fmt.Fprintf(w, "Session %s has no messages.\n", at.Session)
		return nil
	}
	tree, err := s.Tree(at.Session)
	if err != nil {
		return err
	}
	start := 0
	if options.LastTurns > 0 {
		start = lastTurnsStart(h, options.LastTurns)
	}
	knownCalls := make(map[string]core.ToolCall)
	if start > 0 {
		fmt.Fprintf(w, "Conversation %s, last %s:\n\n", at, core.Plural(options.LastTurns, "turn"))
		// A shown result names its call, which may have been made before
		// the first message shown.
		for _, c := range h.Contents[:start] {
			if c.Tools != nil {
				for _, call := range c.Tools.Calls {
					knownCalls[call.ID] = call
				}
			}
		}
	} else {
		fmt.Fprintf(w, "Conversation %s:\n\n", at)
	}
	for i := start; i < len(h.Messages); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > start {
			fmt.Fprintln(w, "\n---")
		}
		showMessage(w, h.Messages[i], h.Contents[i], tree, knownCalls, options)
	}
	return nil
}

func showSystemOnly(w io.Writer, at Ref, settings Settings) {
	switch {
	case settings.System == nil:
		fmt.Fprintf(w, "Conversation %s uses the built-in system prompt, which each run builds from the tools it advertises.\n", at)
	case *settings.System == "":
		fmt.Fprintf(w, "Conversation %s has an empty system prompt; a run with tools sends the texts its tools add.\n", at)
	default:
		fmt.Fprintf(w, "System prompt of conversation %s:\n%s\n", at, *settings.System)
	}
}

// lastTurnsStart finds where the last n turns begin: the nth user message
// from the end that is not a round of tool results or a pin event. Fewer
// turns show all.
func lastTurnsStart(h History, n int) int {
	found := 0
	for i := len(h.Messages) - 1; i >= 0; i-- {
		if h.Messages[i].Role != core.RoleUser || isToolResults(h.Contents[i]) || h.Messages[i].Pin != nil {
			continue
		}
		if found++; found == n {
			return i
		}
	}
	return 0
}

func isToolResults(c Content) bool {
	return c.Tools != nil && len(c.Tools.Results) > 0
}

// Kind names what a message is, for reports of what a run saved.
func Kind(m Message, c Content) string {
	switch {
	case m.Settings != nil:
		return "settings"
	case m.Pin != nil:
		return "pin event"
	case isToolResults(c) && len(c.Tools.Results) == 1:
		return "1 tool result"
	case isToolResults(c):
		return fmt.Sprintf("%d tool results", len(c.Tools.Results))
	case c.Tools != nil && len(c.Tools.Calls) > 0:
		return "assistant response and pending tool calls"
	case m.Role == core.RoleAssistant:
		return "assistant response"
	case m.Role == core.RoleUser:
		return "user input"
	case m.Role == core.RoleDeveloper:
		return "developer instruction"
	}
	return string(m.Role) + " message"
}

func showMessage(w io.Writer, m Message, c Content, tree Tree, knownCalls map[string]core.ToolCall, options ShowOptions) {
	label := FormatRole(string(m.Role), m.Model)
	switch {
	case m.Settings != nil:
		label = "settings"
	case m.Pin != nil:
		label = "pin " + m.Pin.Action
	case isToolResults(c):
		label = "tool results"
	}
	place := m.ID + " after " + m.Parent
	if m.Parent == "" {
		place = m.ID + ", the first message"
	}
	header := fmt.Sprintf("[%s] %s · %s", label, m.Time.Local().Format("2006-01-02 15:04:05"), place)
	var others []string
	for _, id := range tree.Children[m.Parent] {
		if id != m.ID {
			others = append(others, id)
		}
	}
	if len(others) > 0 {
		header += " · alternatives " + strings.Join(others, ", ")
	}
	fmt.Fprintln(w, header)
	if m.Settings != nil {
		showSettings(w, *m.Settings, options)
		return
	}
	if m.Role == core.RoleAssistant && options.Thinking {
		showThinking(w, c.Blocks)
	}
	var userImages []core.ImageBlock
	byResult := map[string][]core.ImageBlock{}
	for _, block := range c.Blocks {
		switch b := block.(type) {
		case core.TextBlock:
			if !isToolResults(c) || strings.TrimSpace(b.Text) != "" {
				if text := strings.TrimRight(b.Text, "\n"); text != "" {
					fmt.Fprintln(w, text)
				}
			}
		case core.ImageBlock:
			userImages = append(userImages, b)
		case core.ToolResultBlock:
			byResult[b.ToolUseID] = append(byResult[b.ToolUseID], b.Images...)
		}
	}
	for _, image := range userImages {
		fmt.Fprintf(w, "[image: %s]\n", core.DescribeImageBlock(image))
	}
	if c.Tools != nil {
		showTools(w, c.Tools, byResult, knownCalls, options.Columns)
	}
}

// showSettings lists what a settings record sets.
func showSettings(w io.Writer, s Settings, options ShowOptions) {
	if cp := s.ManualCheckpoint; cp != nil {
		_, id, _ := strings.Cut(cp.Prefix, "/")
		fmt.Fprintf(w, "context compacted through message %s\n", id)
	}
	switch {
	case s.System == nil:
		fmt.Fprintln(w, "system prompt: built-in")
	case *s.System == "":
		fmt.Fprintln(w, "system prompt: empty")
	case options.SystemSummary && strings.Count(*s.System, "\n") >= 3:
		fmt.Fprintf(w, "system prompt: %d lines; /show system prints it\n", 1+strings.Count(*s.System, "\n"))
	default:
		fmt.Fprintf(w, "system prompt: %s\n", *s.System)
	}
	if s.Notebook != "" {
		view := s.View
		if view == "" {
			view = "catalog"
		}
		fmt.Fprintf(w, "notebook: %s (%s view)\n", s.Notebook, view)
	}
	for _, field := range []struct{ name, value string }{
		{"context policy", s.Policy}, {"context format", s.Format},
	} {
		if field.value != "" {
			fmt.Fprintf(w, "%s: %s\n", field.name, field.value)
		}
	}
	if s.Window != 0 {
		fmt.Fprintf(w, "context window cap: %d\n", s.Window)
	}
	if s.Trigger != 0 {
		fmt.Fprintf(w, "context trigger: %d\n", s.Trigger)
	}
	if s.Early != nil {
		fmt.Fprintf(w, "early cuts: %t\n", *s.Early)
	}
}

// showTools renders a message's tool calls and results. A result's images
// come from its block, printed under the output they arrived with.
func showTools(w io.Writer, interaction *core.ToolInteraction, images map[string][]core.ImageBlock, byID map[string]core.ToolCall, columns func() int) {
	renderer := tools.NewCLIToolUIWithOptions(ui.NewNotifierWithWriter(w), tools.Options{Columns: columns})
	for i, call := range interaction.Calls {
		renderer.ShowCall(i, len(interaction.Calls), call, nil)
		byID[call.ID] = call
	}
	if len(interaction.Results) == 0 {
		return
	}
	calls := make([]core.ToolCall, len(interaction.Results))
	results := append([]core.ToolResult(nil), interaction.Results...)
	for i, result := range results {
		calls[i] = byID[result.ID]
		if calls[i].Name == "" {
			calls[i].Name = result.ID
		}
		results[i].Images = images[result.ID]
	}
	renderer.AfterExecute(calls, results)
}

// showThinking shows the visible reasoning text saved with an answer,
// quoted between the markers a live turn uses. Opaque reasoning shows as
// omitted, and consecutive Responses reasoning items share one section, as
// they did live.
func showThinking(w io.Writer, blocks []core.Block) {
	var section []string
	open := false
	flush := func() {
		if !open {
			return
		}
		text := strings.Join(section, "\n")
		if text == "" {
			text = "[omitted by provider]"
		}
		fmt.Fprintln(w, "--- thinking ---")
		for _, line := range strings.Split(text, "\n") {
			if line == "" {
				fmt.Fprintln(w, "|")
			} else {
				fmt.Fprintf(w, "| %s\n", line)
			}
		}
		fmt.Fprintln(w, "--- end thinking ---")
		section, open = nil, false
	}
	var previous core.Block
	for _, block := range blocks {
		reasoning, isReasoning := block.(core.ReasoningBlock)
		if !isReasoning {
			flush()
		} else {
			if !core.ContinuesReasoningSection(previous, reasoning) {
				flush()
			}
			open = true
			if text := strings.TrimRight(core.ReasoningTextForDisplay(reasoning), "\n"); text != "" {
				section = append(section, text)
			}
		}
		previous = block
	}
	flush()
}
