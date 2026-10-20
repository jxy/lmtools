package session

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/core"
)

// ShowSessionTo respects the conversation's pinned head, including its identity,
// and keeps the check and sidecar reads under the same tree lock as request reads.
func ShowSessionTo(ctx context.Context, w io.Writer, sess *Session, notifier core.Notifier) error {
	return readThroughHead(ctx, DefaultManager(), sess.Path, sess.Head, func(refs []lineageMessageRef, _ *MessageRef) error {
		if len(refs) == 0 {
			fmt.Fprintln(w, "No messages in conversation.")
			return nil
		}
		fmt.Fprintf(w, "Conversation %s:\n\n", GetSessionID(sess.Path))
		knownCalls := make(map[string]core.ToolCall)
		for i, ref := range refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if i > 0 {
				fmt.Fprintln(w, "\n---")
			}
			if err := displayMessageWithToolsTo(w, ref, notifier, knownCalls); err != nil {
				return err
			}
		}
		return nil
	})
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
		if interaction != nil && len(interaction.Results) > 0 {
			kind = fmt.Sprintf("%d tool result(s)", len(interaction.Results))
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
