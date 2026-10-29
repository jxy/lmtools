package session

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/core"
)

// ShowOptions narrow and annotate a conversation display.
type ShowOptions struct {
	// LastTurns, when positive, shows only that many of the latest turns. A
	// turn starts at a user message other than a round of tool results.
	LastTurns int
	// SystemSummary shows the system prompt as one line saying how long it
	// is; SystemOnly shows nothing but the system prompt, in full.
	SystemSummary bool
	SystemOnly    bool
	// Thinking shows the reasoning text saved with each answer.
	Thinking bool
	// Columns, when set, is the terminal's width, within which tool rounds
	// wrap their lines as they do live.
	Columns func() int
}

// ShowSessionTo respects the conversation's pinned head, including its identity,
// and keeps the check and sidecar reads under the same tree lock as request reads.
func ShowSessionTo(ctx context.Context, w io.Writer, sess *Session, notifier core.Notifier) error {
	return ShowSessionWithOptions(ctx, w, sess, notifier, ShowOptions{})
}

// ShowSessionWithOptions is ShowSessionTo narrowed or annotated by options.
func ShowSessionWithOptions(ctx context.Context, w io.Writer, sess *Session, notifier core.Notifier, options ShowOptions) error {
	return readThroughHead(ctx, DefaultManager(), sess.Path, sess.Head, func(refs []lineageMessageRef, _ *MessageRef) error {
		if len(refs) == 0 {
			fmt.Fprintln(w, "No messages in conversation.")
			return nil
		}
		id := GetSessionID(sess.Path)
		if options.SystemOnly {
			for _, ref := range refs {
				if ref.message.Role == core.RoleSystem {
					fmt.Fprintf(w, "System prompt of conversation %s:\n%s\n", id, ref.message.Content)
					return nil
				}
			}
			fmt.Fprintf(w, "Conversation %s has no system prompt.\n", id)
			return nil
		}
		// Each message's tool calls or results are read once: for where the
		// turns begin, for the calls a shown result names, and for display.
		interactions := make([]*core.ToolInteraction, len(refs))
		for i, ref := range refs {
			interaction, err := LoadToolInteraction(ref.path, ref.message.ID)
			if err != nil {
				return err
			}
			interactions[i] = interaction
		}
		start := 0
		if options.LastTurns > 0 {
			start = lastTurnsStart(refs, interactions, options.LastTurns)
		}
		knownCalls := make(map[string]core.ToolCall)
		if start > 0 {
			fmt.Fprintf(w, "Conversation %s, last %s:\n\n", id, core.Plural(options.LastTurns, "turn"))
			// A shown result names its call, which may have been made
			// before the first message shown.
			for _, interaction := range interactions[:start] {
				if interaction != nil {
					for _, call := range interaction.Calls {
						knownCalls[call.ID] = call
					}
				}
			}
		} else {
			fmt.Fprintf(w, "Conversation %s:\n\n", id)
		}
		for i, ref := range refs[start:] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if i > 0 {
				fmt.Fprintln(w, "\n---")
			}
			if err := displayMessageWithToolsTo(w, ref, interactions[start+i], notifier, knownCalls, options); err != nil {
				return err
			}
		}
		return nil
	})
}

// lastTurnsStart finds where the last n turns begin: the nth user message
// from the end that is not a round of tool results. Fewer turns show all.
func lastTurnsStart(refs []lineageMessageRef, interactions []*core.ToolInteraction, n int) int {
	found := 0
	for i := len(refs) - 1; i >= 0; i-- {
		if refs[i].message.Role != core.RoleUser || isToolResults(refs[i].message, interactions[i]) {
			continue
		}
		if found++; found == n {
			return i
		}
	}
	return 0
}

// isToolResults reports a user message that carries a round of tool results,
// which the provider wire calls a user turn and a person does not. Its text,
// when it has any, is the round's truncation notes.
func isToolResults(msg Message, interaction *core.ToolInteraction) bool {
	return msg.Role == core.RoleUser && interaction != nil && len(interaction.Results) > 0
}

// SavedHeadKind describes the last committed record without inferring success
// from text that happened to reach the terminal.
func SavedHeadKind(ctx context.Context, sess *Session) (string, error) {
	kind := "records"
	err := readThroughHead(ctx, DefaultManager(), sess.Path, sess.Head, func(refs []lineageMessageRef, _ *MessageRef) error {
		if len(refs) == 0 {
			return nil
		}
		last := refs[len(refs)-1]
		interaction, err := LoadToolInteraction(last.path, last.message.ID)
		if err != nil {
			return err
		}
		if interaction != nil && len(interaction.Results) == 1 {
			kind = "1 tool result"
		} else if interaction != nil && len(interaction.Results) > 1 {
			kind = fmt.Sprintf("%d tool results", len(interaction.Results))
		} else if interaction != nil && len(interaction.Calls) > 0 {
			kind = "assistant response and pending tool calls"
		} else {
			switch last.message.Role {
			case core.RoleAssistant:
				kind = "assistant response"
			case core.RoleUser:
				kind = "user input"
			case core.RoleDeveloper:
				kind = "developer instruction"
			default:
				kind = string(last.message.Role) + " message"
			}
		}
		return nil
	})
	return kind, err
}
