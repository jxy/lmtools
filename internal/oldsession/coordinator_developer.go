package oldsession

import (
	"context"
	"lmtools/internal/core"
	"strings"
)

func validateDeveloperLineage(cfg core.RequestOptions, refs []lineageMessageRef) error {
	for _, ref := range refs {
		if ref.message.Role == core.RoleDeveloper {
			return core.ValidateDeveloperRole(cfg)
		}
	}
	return nil
}

// appendPlannedDeveloperMessage puts this invocation's instruction immediately
// before its new user turn. A failed earlier commit can have saved the
// instruction alone: reuse that pending head instead of adding it twice.
func (c *requestPreparer) appendPlannedDeveloperMessage(messages []core.TypedMessage) []core.TypedMessage {
	if c.cfg.Developer == "" {
		return messages
	}
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Role == string(core.RoleDeveloper) && len(last.Blocks) == 1 {
			if text, ok := last.Blocks[0].(core.TextBlock); ok && text.Text == c.cfg.Developer {
				// requestPreparer owns its options copy. Only this plan reuses
				// the head; the caller's pending instruction stays until commit.
				c.cfg.Developer = ""
				return messages
			}
		}
	}
	return append(messages, core.NewTextMessage(string(core.RoleDeveloper), c.cfg.Developer))
}

func (c *requestPreparer) appendUserTurn(ctx context.Context, sess *Session, user Message, blocks []core.Block) (SaveResult, error) {
	if c.cfg.Developer == "" {
		return AppendMessageWithBlocks(ctx, sess, user, nil, nil, blocks)
	}
	developer := Message{
		Role: core.RoleDeveloper, Content: c.cfg.Developer, Timestamp: user.Timestamp,
	}
	return AppendMessagesWithBlocks(ctx, sess, []MessageEntry{
		{Message: developer, Blocks: core.NewTextMessage(string(core.RoleDeveloper), c.cfg.Developer).Blocks},
		{Message: user, Blocks: blocks},
	})
}

// userBranchRefs replaces a user turn while preserving developer messages
// and local pin changes between it and the preceding answer. An unanswered
// question there stays when a pin still in force quotes it.
func userBranchRefs(refs []lineageMessageRef, previousAssistant int) ([]lineageMessageRef, error) {
	prefix := systemPrefix(refs)
	if previousAssistant >= 0 {
		prefix = refs[:previousAssistant+1]
	}
	tail := refs[previousAssistant+1:]
	// Only the latest change to a pin decides whether it still cites a source.
	cited := map[string]bool{}
	seen := map[string]bool{}
	for i := len(tail) - 1; i >= 0; i-- {
		if pin := tail[i].message.Pin; pin != nil && !seen[pin.ID] {
			seen[pin.ID] = true
			if pin.Action != "remove" && pin.Source != "" {
				id, _, _ := strings.Cut(pin.Source, ".")
				cited[id] = true
			}
		}
	}
	retained := append([]lineageMessageRef(nil), prefix...)
	for _, ref := range tail {
		keep := ref.message.Role == core.RoleDeveloper || ref.message.Pin != nil
		if !keep && len(cited) > 0 && ref.message.Role == core.RoleUser {
			source, err := lineageSource(ref)
			if err != nil {
				return nil, err
			}
			keep = cited[source]
		}
		if keep {
			retained = append(retained, ref)
		}
	}
	return retained, nil
}

// lineageSource is the source a pin uses to cite a user message. Only a
// record saved before revisions existed needs its blocks to compute it.
func lineageSource(ref lineageMessageRef) (string, error) {
	var msg core.TypedMessage
	if ref.message.Origin == "" && ref.message.Revision == "" {
		var err error
		if msg, err = buildTypedMessageFromLineageRef(ref, map[string]string{}); err != nil {
			return "", err
		}
	}
	return sourceRef(ref, msg).Source, nil
}
