package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"path/filepath"
	"strconv"
)

// MessageRef identifies a committed message by the directory that owns its
// metadata file and its local message ID. Revision, when set, is the
// identity of the message the ref was taken from, which another message
// under the same path and ID does not share; see Pinned Heads. A ref without
// one, such as LastMessageRefWithManager returns, names whatever message
// holds the path and ID.
type MessageRef struct {
	Path     string
	ID       string
	Revision string
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

// buildBranchRequestMessages returns the request messages for branching at
// branchRef, the anchor's role, and the head of that lineage: the last
// message the request carries, which the branch's writes follow. The scan,
// the sidecars, and the head's identity are read under one hold of the
// tree's lock.
func buildBranchRequestMessages(ctx context.Context, branchRef string) ([]core.TypedMessage, core.Role, *MessageRef, error) {
	manager := DefaultManager()
	sessionPath, messageID := manager.ParseMessageID(branchRef)
	if messageID == "" {
		return nil, "", nil, errors.WrapError("parse branch reference", fmt.Errorf("branch reference must point to a message: %s", branchRef))
	}

	sessionPath = manager.ResolveSessionPath(sessionPath)
	anchorPath, anchorID := GetAnchorForBranching(sessionPath, messageID)
	anchorPath = manager.ResolveSessionPath(anchorPath)

	var (
		messages   []core.TypedMessage
		anchorRole core.Role
		head       *MessageRef
	)
	err := withTreeLock(anchorPath, func() error {
		var err error
		messages, anchorRole, head, err = branchRequestMessagesLocked(ctx, manager, anchorPath, anchorID)
		return err
	})
	if err != nil {
		return nil, "", nil, err
	}
	return messages, anchorRole, head, nil
}

func branchRequestMessagesLocked(ctx context.Context, manager *Manager, anchorPath, anchorID string) ([]core.TypedMessage, core.Role, *MessageRef, error) {
	refs, err := lineageMessageRefsWithManager(manager, anchorPath)
	if err != nil {
		return nil, "", nil, err
	}

	anchorIdx := -1
	for i, ref := range refs {
		if ref.path == anchorPath && ref.message.ID == anchorID {
			anchorIdx = i
			break
		}
	}
	if anchorIdx == -1 {
		return nil, "", nil, errors.WrapError("find branch anchor", fmt.Errorf("message %s was not found in lineage for %s", anchorID, anchorPath))
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
		return nil, "", nil, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", anchorRole, anchorID))
	}

	messages, err := buildTypedMessagesFromLineageRefs(ctx, refs)
	if err != nil {
		return nil, "", nil, err
	}
	head, err := pinHeadLocked(refs)
	if err != nil {
		return nil, "", nil, err
	}
	return messages, anchorRole, head, nil
}

// ForkSessionThroughMessageWithManager creates a new session containing only
// the visible lineage through a specific committed message snapshot.
func ForkSessionThroughMessageWithManager(ctx context.Context, manager *Manager, sessionPath, terminalPath, terminalMessageID string, newSystemPrompt *string) (*Session, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)

	return buildFork(ctx, manager, sessionPath, func() (forkSource, error) {
		refs, err := lineageMessageRefsThroughMessageWithManager(manager, sessionPath, terminalPath, terminalMessageID)
		if err != nil {
			return forkSource{}, err
		}
		source := forkSource{refs: refs}
		if newSystemPrompt != nil {
			source.system = *newSystemPrompt
		}
		return source, nil
	})
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

// lineageScan is one walk of a session lineage.
type lineageScan struct {
	refs []lineageMessageRef
	// activeIDs is every message ID the active (leaf) directory listed during
	// this walk, including any the walk skipped as unreadable. Append detection
	// compares against it rather than against the IDs that made it into refs,
	// so a message this walk already considered is never mistaken for a later
	// append, while a message committed after the walk always is.
	activeIDs []string
}

func lineageMessageRefsWithManager(manager *Manager, sessionPath string) ([]lineageMessageRef, error) {
	scan, err := scanLineage(manager, sessionPath)
	if err != nil {
		return nil, err
	}
	return scan.refs, nil
}

func scanLineage(manager *Manager, sessionPath string) (lineageScan, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)

	rootDir, components := manager.ParseSessionPath(sessionPath)
	load := func(dir string) ([]lineageMessageRef, []string, error) {
		msgs, listed, err := loadMessagesInDirWithListing(dir)
		if err != nil {
			return nil, nil, errors.WrapError("load messages in "+dir, err)
		}
		refs := make([]lineageMessageRef, 0, len(msgs))
		for _, msg := range msgs {
			refs = append(refs, lineageMessageRef{path: dir, message: msg})
		}
		return refs, listed, nil
	}

	var lineage []lineageMessageRef
	var lastAssistant *lineageMessageRef
	assistantAlreadyInLineage := false
	dir := rootDir

	for i := 0; ; i++ {
		refs, listed, err := load(dir)
		if err != nil {
			return lineageScan{}, err
		}

		if i == len(components) {
			lineage = append(lineage, refs...)
			return lineageScan{refs: lineage, activeIDs: listed}, nil
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
			return lineageScan{}, errors.WrapError("find branch point", fmt.Errorf("branch point %s not found in %s", branchMsgID, dir))
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
			return lineageScan{}, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", branchMsg.message.Role, branchMsg.message.ID))
		}

		dir = filepath.Join(dir, comp)
	}
}

func buildTypedMessagesFromLineageRefs(ctx context.Context, refs []lineageMessageRef) ([]core.TypedMessage, error) {
	var result []core.TypedMessage
	toolNamesByID := make(map[string]string)

	for _, ref := range refs {
		message, err := buildTypedMessageFromLineageRef(ref, toolNamesByID)
		if err != nil {
			return nil, err
		}
		result = append(result, message)
	}

	return result, nil
}

// buildTypedMessageFromLineageRef reconstructs one committed message and folds
// its tool names into the running index, which later messages read to name
// results the writer left anonymous.
func buildTypedMessageFromLineageRef(ref lineageMessageRef, toolNamesByID map[string]string) (core.TypedMessage, error) {
	msg := ref.message
	toolInteraction, err := LoadToolInteraction(ref.path, msg.ID)
	if err != nil {
		return core.TypedMessage{}, errors.WrapError("load tool interaction for message "+msg.ID, err)
	}

	blocks, ok, err := loadMessageBlocks(ref.path, msg.ID)
	if err != nil {
		return core.TypedMessage{}, err
	}
	if ok {
		return core.TypedMessage{
			Role:   string(msg.Role),
			Blocks: applyToolNameIndex(blocks, toolNamesByID),
		}, nil
	}

	return buildTypedMessage(msg, toolInteraction, toolNamesByID), nil
}

func pendingToolCallsFromLineageRefs(ctx context.Context, refs []lineageMessageRef) ([]core.ToolCall, error) {
	calls, _, err := pendingToolCallsWithRef(ctx, refs)
	return calls, err
}

// pendingToolCallsWithRef returns the tool calls pending at the end of refs
// and the assistant message that holds them.
func pendingToolCallsWithRef(_ context.Context, refs []lineageMessageRef) ([]core.ToolCall, lineageMessageRef, error) {
	if len(refs) == 0 {
		return nil, lineageMessageRef{}, nil
	}

	resolved := make(map[string]bool)
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		toolInteraction, err := LoadToolInteraction(ref.path, ref.message.ID)
		if err != nil {
			return nil, lineageMessageRef{}, errors.WrapError("load tool interaction for message "+ref.message.ID, err)
		}

		if ref.message.Role == core.RoleAssistant && toolInteraction != nil && len(toolInteraction.Calls) > 0 {
			pending := unresolvedToolCalls(toolInteraction, resolved)
			if len(pending) == 0 {
				return nil, lineageMessageRef{}, nil
			}
			return pending, ref, nil
		}

		for _, res := range toolInteractionResults(toolInteraction) {
			if res.ID != "" {
				resolved[res.ID] = true
			}
		}

		if ref.message.Role == core.RoleUser && (toolInteraction == nil || len(toolInteraction.Results) == 0) {
			return nil, lineageMessageRef{}, nil
		}
	}
	return nil, lineageMessageRef{}, nil
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

// copyLineageMessageRefs copies refs, leaving out their system message, into
// fork, whose lock the caller holds and whose last message is head, and
// returns the fork's last message afterwards. Each copy is a new commit with
// a revision of its own; its tool interaction and blocks, invocation
// identities included, are copied as they are.
func copyLineageMessageRefs(ctx context.Context, refs []lineageMessageRef, fork *Session, head MessageRef) (MessageRef, error) {
	id := uint64(0)
	if head.ID != "" {
		last, err := strconv.ParseUint(head.ID, 16, 64)
		if err != nil {
			return MessageRef{}, errors.WrapError("parse message ID "+head.ID, err)
		}
		id = last + 1
	}
	for _, ref := range refs {
		msg := ref.message
		if msg.Role == core.RoleSystem {
			continue
		}

		// A copy is exact or it fails. Copying a message without its tool
		// calls separates them from their results, and without its blocks
		// drops images and reasoning, so an unreadable file is an error.
		toolInteraction, err := LoadToolInteraction(ref.path, msg.ID)
		if err != nil {
			return MessageRef{}, errors.WrapError("copy tool interaction of message "+msg.ID, err)
		}

		var blocks []core.Block
		loadedBlocks, ok, err := loadMessageBlocks(ref.path, msg.ID)
		if err != nil {
			return MessageRef{}, errors.WrapError("copy typed blocks of message "+msg.ID, err)
		}
		if ok {
			blocks = loadedBlocks
		}

		newMsg := Message{
			Role:             msg.Role,
			Content:          msg.Content,
			ThoughtSignature: msg.ThoughtSignature,
			Timestamp:        msg.Timestamp,
			Model:            msg.Model,
		}
		staged, err := stageMessageFilesWithBlocks(fork.Path, newMsg, toolInteraction, blocks)
		if err != nil {
			return MessageRef{}, errors.WrapError("stage message", err)
		}
		newMsgID := formatVariableWidthHexID(int(id))
		err = commitStagedMessageLocked(ctx, fork.Path, newMsgID, staged)
		staged.Close()
		if err != nil {
			return MessageRef{}, errors.WrapError("place message", err)
		}
		head = MessageRef{Path: fork.Path, ID: newMsgID, Revision: staged.Revision}
		id++
		logger.From(ctx).Debugf("Copied message %s -> %s (role=%s, hasTools=%v)", msg.ID, newMsgID, msg.Role, toolInteraction != nil)
	}
	return head, nil
}
