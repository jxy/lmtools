package session

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
)

// MessageRef identifies a committed message by the directory that owns its
// metadata file and its local message ID.
type MessageRef struct {
	Path string
	ID   string
}

type lineageMessageRef struct {
	path    string
	message Message
}

// LastMessageRefWithManager returns the last message visible along a session
// lineage, including messages inherited from parent branches.
func LastMessageRefWithManager(manager *Manager, sessionPath string) (MessageRef, bool, error) {
	refs, err := lineageMessageRefsWithManager(manager, sessionPath)
	if err != nil {
		return MessageRef{}, false, err
	}
	if len(refs) == 0 {
		return MessageRef{}, false, nil
	}
	last := refs[len(refs)-1]
	return MessageRef{Path: last.path, ID: last.message.ID}, true, nil
}

// BuildMessagesWithToolInteractionsThroughMessageWithManager reconstructs the
// visible lineage through a specific committed message snapshot.
func BuildMessagesWithToolInteractionsThroughMessageWithManager(ctx context.Context, manager *Manager, sessionPath, terminalPath, terminalMessageID string) ([]core.TypedMessage, error) {
	refs, err := lineageMessageRefsThroughMessageWithManager(manager, sessionPath, terminalPath, terminalMessageID)
	if err != nil {
		return nil, err
	}
	return buildTypedMessagesFromLineageRefs(ctx, refs)
}

func buildBranchRequestMessages(ctx context.Context, branchRef string) ([]core.TypedMessage, core.Role, error) {
	manager := DefaultManager()
	sessionPath, messageID := manager.ParseMessageID(branchRef)
	if messageID == "" {
		return nil, "", errors.WrapError("parse branch reference", fmt.Errorf("branch reference must point to a message: %s", branchRef))
	}

	sessionPath = manager.ResolveSessionPath(sessionPath)
	anchorPath, anchorID := GetAnchorForBranching(sessionPath, messageID)
	anchorPath = manager.ResolveSessionPath(anchorPath)

	refs, err := lineageMessageRefsWithManager(manager, anchorPath)
	if err != nil {
		return nil, "", err
	}

	anchorIdx := -1
	for i, ref := range refs {
		if ref.path == anchorPath && ref.message.ID == anchorID {
			anchorIdx = i
			break
		}
	}
	if anchorIdx == -1 {
		return nil, "", errors.WrapError("find branch anchor", fmt.Errorf("message %s was not found in lineage for %s", anchorID, anchorPath))
	}

	anchorRole := refs[anchorIdx].message.Role
	switch anchorRole {
	case core.RoleAssistant:
		refs = refs[:anchorIdx]
	case core.RoleUser:
		prevAssistantIdx := -1
		for i := anchorIdx - 1; i >= 0; i-- {
			if refs[i].message.Role == core.RoleAssistant {
				prevAssistantIdx = i
				break
			}
		}
		if prevAssistantIdx == -1 {
			refs = nil
		} else {
			refs = refs[:prevAssistantIdx+1]
		}
	default:
		return nil, "", errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", anchorRole, anchorID))
	}

	messages, err := buildTypedMessagesFromLineageRefs(ctx, refs)
	if err != nil {
		return nil, "", err
	}
	return messages, anchorRole, nil
}

// ForkSessionThroughMessageWithManager creates a new session containing only
// the visible lineage through a specific committed message snapshot.
func ForkSessionThroughMessageWithManager(ctx context.Context, manager *Manager, sessionPath, terminalPath, terminalMessageID string, newSystemPrompt *string) (*Session, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)

	var (
		newSession *Session
		err        error
	)
	if newSystemPrompt != nil {
		newSession, err = manager.CreateSession(*newSystemPrompt, logger.From(ctx))
	} else {
		newSession, err = manager.CreateSession("", logger.From(ctx))
	}
	if err != nil {
		return nil, errors.WrapError("create new session", err)
	}

	refs, err := lineageMessageRefsThroughMessageWithManager(manager, sessionPath, terminalPath, terminalMessageID)
	if err != nil {
		_ = os.RemoveAll(newSession.Path)
		return nil, err
	}
	if err := copyLineageMessageRefs(ctx, refs, newSession); err != nil {
		_ = os.RemoveAll(newSession.Path)
		return nil, err
	}

	return newSession, nil
}

func lineageMessageRefsThroughMessageWithManager(manager *Manager, sessionPath, terminalPath, terminalMessageID string) ([]lineageMessageRef, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)
	if terminalMessageID == "" {
		return nil, nil
	}
	terminalPath = manager.ResolveSessionPath(terminalPath)

	refs, err := lineageMessageRefsWithManager(manager, sessionPath)
	if err != nil {
		return nil, err
	}
	for i, ref := range refs {
		if ref.path == terminalPath && ref.message.ID == terminalMessageID {
			return refs[:i+1], nil
		}
	}
	return nil, errors.WrapError(
		"find terminal message",
		fmt.Errorf("message %s was not found in lineage for %s", terminalMessageID, sessionPath),
	)
}

func lineageMessageRefsWithManager(manager *Manager, sessionPath string) ([]lineageMessageRef, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)

	rootDir, components := manager.ParseSessionPath(sessionPath)
	load := func(dir string) ([]lineageMessageRef, error) {
		msgs, err := loadMessagesInDir(dir)
		if err != nil {
			return nil, errors.WrapError("load messages in "+dir, err)
		}
		refs := make([]lineageMessageRef, 0, len(msgs))
		for _, msg := range msgs {
			refs = append(refs, lineageMessageRef{path: dir, message: msg})
		}
		return refs, nil
	}

	var lineage []lineageMessageRef
	var lastAssistant *lineageMessageRef
	assistantAlreadyInLineage := false
	dir := rootDir

	for i := 0; ; i++ {
		refs, err := load(dir)
		if err != nil {
			return nil, err
		}

		if i == len(components) {
			lineage = append(lineage, refs...)
			break
		}

		comp := components[i]
		_, branchMsgID, _ := IsSiblingDir(comp)
		branchIdx := -1
		for j := range refs {
			if refs[j].message.ID == branchMsgID {
				branchIdx = j
				break
			}
		}
		if branchIdx == -1 {
			return nil, errors.WrapError("find branch point", fmt.Errorf("branch point %s not found in %s", branchMsgID, dir))
		}

		branchMsg := refs[branchIdx]
		switch branchMsg.message.Role {
		case core.RoleAssistant:
			lineage = append(lineage, refs[:branchIdx]...)
			lastAssistant = &branchMsg
			assistantAlreadyInLineage = false

		case core.RoleUser:
			prevAssistIdx := -1
			for j := branchIdx - 1; j >= 0; j-- {
				if refs[j].message.Role == core.RoleAssistant {
					prevAssistIdx = j
					break
				}
			}

			if prevAssistIdx != -1 {
				lineage = append(lineage, refs[:prevAssistIdx+1]...)
				prevAssistant := refs[prevAssistIdx]
				lastAssistant = &prevAssistant
				assistantAlreadyInLineage = true
			} else if lastAssistant != nil && !assistantAlreadyInLineage {
				lineage = append(lineage, *lastAssistant)
				assistantAlreadyInLineage = true
			}

		default:
			return nil, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", branchMsg.message.Role, branchMsg.message.ID))
		}

		dir = filepath.Join(dir, comp)
	}

	return lineage, nil
}

func buildTypedMessagesFromLineageRefs(ctx context.Context, refs []lineageMessageRef) ([]core.TypedMessage, error) {
	var result []core.TypedMessage
	toolNamesByID := make(map[string]string)

	for _, ref := range refs {
		msg := ref.message
		toolInteraction, err := LoadToolInteraction(ref.path, msg.ID)
		if err != nil {
			return nil, errors.WrapError("load tool interaction for message "+msg.ID, err)
		}

		if blocks, ok, err := loadMessageBlocks(ref.path, msg.ID); err != nil {
			return nil, err
		} else if ok {
			result = append(result, core.TypedMessage{
				Role:   string(msg.Role),
				Blocks: applyToolNameIndex(blocks, toolNamesByID),
			})
			continue
		}

		result = append(result, buildTypedMessage(msg, toolInteraction, toolNamesByID))
	}

	return result, nil
}

func pendingToolCallsFromLineageRefs(ctx context.Context, refs []lineageMessageRef) ([]core.ToolCall, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	resolved := make(map[string]bool)
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		toolInteraction, err := LoadToolInteraction(ref.path, ref.message.ID)
		if err != nil {
			return nil, errors.WrapError("load tool interaction for message "+ref.message.ID, err)
		}

		if ref.message.Role == core.RoleAssistant && toolInteraction != nil && len(toolInteraction.Calls) > 0 {
			pending := unresolvedToolCalls(toolInteraction, resolved)
			if len(pending) == 0 {
				return nil, nil
			}
			return pending, nil
		}

		for _, res := range toolInteractionResults(toolInteraction) {
			if res.ID != "" {
				resolved[res.ID] = true
			}
		}

		if ref.message.Role == core.RoleUser && (toolInteraction == nil || len(toolInteraction.Results) == 0) {
			return nil, nil
		}
	}
	return nil, nil
}

func unresolvedToolCalls(toolInteraction *core.ToolInteraction, resolved map[string]bool) []core.ToolCall {
	if toolInteraction == nil || len(toolInteraction.Calls) == 0 {
		return nil
	}

	localResolved := make(map[string]bool, len(resolved)+len(toolInteraction.Results))
	for id, ok := range resolved {
		if ok {
			localResolved[id] = true
		}
	}
	for _, res := range toolInteraction.Results {
		if res.ID != "" {
			localResolved[res.ID] = true
		}
	}

	pending := make([]core.ToolCall, 0, len(toolInteraction.Calls))
	for _, call := range toolInteraction.Calls {
		if call.ID == "" || !localResolved[call.ID] {
			pending = append(pending, call)
		}
	}
	return pending
}

func toolInteractionResults(toolInteraction *core.ToolInteraction) []core.ToolResult {
	if toolInteraction == nil {
		return nil
	}
	return toolInteraction.Results
}

func copyLineageMessageRefs(ctx context.Context, refs []lineageMessageRef, newSession *Session) error {
	mc := newMessageCommitter(newSession.Path)
	for _, ref := range refs {
		msg := ref.message
		if msg.Role == core.RoleSystem {
			continue
		}

		var toolInteraction *core.ToolInteraction
		ti, err := LoadToolInteraction(ref.path, msg.ID)
		if err != nil {
			logger.From(ctx).Debugf("Failed to load tool interaction for message %s: %v", msg.ID, err)
		} else if ti != nil {
			toolInteraction = ti
		}

		var blocks []core.Block
		loadedBlocks, ok, err := loadMessageBlocks(ref.path, msg.ID)
		if err != nil {
			logger.From(ctx).Debugf("Failed to load typed blocks for message %s: %v", msg.ID, err)
		} else if ok {
			blocks = loadedBlocks
		}

		newMsg := Message{
			Role:             msg.Role,
			Content:          msg.Content,
			ThoughtSignature: msg.ThoughtSignature,
			Timestamp:        msg.Timestamp,
			Model:            msg.Model,
		}
		staged, err := stageMessageFilesWithBlocks(mc.sessionPath, newMsg, toolInteraction, blocks)
		if err != nil {
			return errors.WrapError("stage message", err)
		}
		newMsgID, needSibling, _, err := mc.Commit(ctx, staged)
		staged.Close()
		if err != nil {
			return errors.WrapError("place message", err)
		}
		if needSibling {
			return errors.WrapError("copy message", stdErrors.New("unexpected conflict when copying message"))
		}
		logger.From(ctx).Debugf("Copied message %s -> %s (role=%s, hasTools=%v)", msg.ID, newMsgID, msg.Role, toolInteraction != nil)
	}
	return nil
}
