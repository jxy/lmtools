package session

import (
	"fmt"
	"io"
	"lmtools/internal/core"
	"strings"
	"unicode/utf8"
)

// List prints every session: its number, identity, creation time and
// message count, then its message tree, one line per message with its ID,
// role and first words, indented where alternatives start. A session in the
// old format is named as one.
func (s *Store) List(w io.Writer) error {
	sessions, err := s.Sessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Fprintln(w, "No sessions found.")
		return nil
	}
	for i, info := range sessions {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if info.Old {
			fmt.Fprintf(w, "%s • older format, which this version cannot read; delete it with -delete %s or move it out of %s\n", info.Number, info.Number, s.Dir())
			continue
		}
		if info.Unreadable != nil {
			fmt.Fprintf(w, "%s • unreadable: %v\n", info.Number, info.Unreadable)
			continue
		}
		tree, err := s.Tree(info.Number)
		if err != nil {
			fmt.Fprintf(w, "%s • unreadable: %v\n", info.Number, err)
			continue
		}
		fmt.Fprintf(w, "%s • %s • %s • %s\n", info.Number, info.Created.Local().Format("2006-01-02 15:04:05"), core.Plural(len(tree.Messages), "message"), info.Identity)
		s.listTree(w, info.Number, tree, "", "")
	}
	return nil
}

// listTree prints the subtree under parent. A message with one child keeps
// its line's indentation; alternatives each start a branch.
func (s *Store) listTree(w io.Writer, number string, tree Tree, parent, indent string) {
	children := tree.Children[parent]
	if len(children) == 1 {
		id := children[0]
		fmt.Fprintf(w, "  %s%s\n", indent, s.listLine(number, tree.Messages[id]))
		s.listTree(w, number, tree, id, indent)
		return
	}
	for i, id := range children {
		branch, rest := "├─ ", "│  "
		if i == len(children)-1 {
			branch, rest = "└─ ", "   "
		}
		fmt.Fprintf(w, "  %s%s%s\n", indent, branch, s.listLine(number, tree.Messages[id]))
		s.listTree(w, number, tree, id, indent+rest)
	}
}

// listLine is one message of the listing: its ID, what it is, and the first
// words it says.
func (s *Store) listLine(number string, m Message) string {
	if m.Settings != nil {
		return m.ID + " settings"
	}
	c, err := s.Content(Ref{Session: number, Message: m.ID})
	if err != nil {
		return m.ID + " " + string(m.Role) + " (unreadable)"
	}
	label := string(m.Role)
	switch {
	case m.Pin != nil:
		label = "pin"
	case isToolResults(c):
		label = "tool results"
	case c.Tools != nil && len(c.Tools.Calls) > 0:
		label = "assistant tool calls"
	}
	var words []string
	for _, block := range c.Blocks {
		if text, ok := block.(core.TextBlock); ok {
			words = append(words, strings.Fields(text.Text)...)
		}
	}
	preview := strings.Join(words, " ")
	const width = 60
	if utf8.RuneCountInString(preview) > width {
		preview = string([]rune(preview)[:width]) + "…"
	}
	if preview == "" {
		return m.ID + " " + label
	}
	return m.ID + " " + label + ": " + preview
}
