package session

import (
	"context"
	"lmtools/internal/core"
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

// userBranchRefs replaces a user turn while preserving the developer messages
// that preceded it, including instructions added after the preceding answer.
func userBranchRefs(refs []lineageMessageRef, previousAssistant int) []lineageMessageRef {
	prefix := systemPrefix(refs)
	if previousAssistant >= 0 {
		prefix = refs[:previousAssistant+1]
	}
	retained := append([]lineageMessageRef(nil), prefix...)
	for _, ref := range refs[previousAssistant+1:] {
		if ref.message.Role == core.RoleDeveloper {
			retained = append(retained, ref)
		}
	}
	return retained
}
