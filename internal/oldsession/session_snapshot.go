package oldsession

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// branchRequest is what preparing a request at a message reads: the
// messages the request carries and where each is stored, the anchor's role,
// the head of that lineage, which the branch's writes follow, the system
// prompt the lineage carries, and the anchor's directory and ID. recorded
// is the anchor role a sibling recorded when the reference bubbled up
// through it, which the request reads in place of the anchor's current role.
// source is the directory of the message the reference names, whose
// notebook binding the request was configured with.
type branchRequest struct {
	refs       []ContextRef
	messages   []core.TypedMessage
	carried    []MessageRef
	anchorRole core.Role
	recorded   core.Role
	head       *MessageRef
	system     *string
	path       string
	anchorID   string
	source     string
}

// buildBranchRequestMessages reads what a request that branches at branchRef
// carries. The scan, the sidecars, and the head's identity are read under one
// hold of the tree's lock.
func buildBranchRequestMessages(ctx context.Context, branchRef string, needRefs bool) (branchRequest, error) {
	manager := DefaultManager()
	sessionPath, messageID := manager.ParseMessageID(branchRef)
	if messageID == "" {
		return branchRequest{}, errors.WrapError("parse branch reference", fmt.Errorf("branch reference must point to a message: %s", branchRef))
	}

	sessionPath = manager.ResolveSessionPath(sessionPath)

	var request branchRequest
	err := withTreeLock(ctx, sessionPath, func() error {
		// A sibling reference bubbles to its original anchor. Validate the
		// requested message first so that resolution cannot hide its role.
		message, err := readMessage(sessionPath, messageID)
		if err != nil {
			return errors.WrapError("read branch message "+messageID, err)
		}
		if err := validateBranchMessageRole(message.Role); err != nil {
			return err
		}
		anchorPath, anchorID := GetAnchorForBranching(sessionPath, messageID)
		anchorPath = manager.ResolveSessionPath(anchorPath)
		recorded, err := bubbledAnchorRole(sessionPath, anchorPath)
		if err != nil {
			return err
		}
		request, err = branchRequestLocked(ctx, manager, anchorPath, anchorID, recorded, needRefs)
		request.source = sessionPath
		return err
	})
	return request, err
}

// bubbledAnchorRole returns the anchor role recorded by the sibling through
// which a reference in sessionPath bubbled up to its anchor in anchorPath.
// It returns none when the reference did not bubble or the sibling has no
// record.
func bubbledAnchorRole(sessionPath, anchorPath string) (core.Role, error) {
	rel, err := filepath.Rel(anchorPath, sessionPath)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", nil
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return recordedAnchorRole(filepath.Join(anchorPath, first))
}

// replacedPosition returns where the message a sibling replaces stands
// among refs, the messages of one directory in ID order: its index, or,
// when it was deleted, the index of the first message after its ID.
func replacedPosition(refs []lineageMessageRef, id string) int {
	want, err := strconv.ParseUint(id, 16, 64)
	for i, ref := range refs {
		if ref.message.ID == id {
			return i
		}
		if got, gotErr := strconv.ParseUint(ref.message.ID, 16, 64); err == nil && gotErr == nil && got > want {
			return i
		}
	}
	return len(refs)
}

func validateBranchMessageRole(role core.Role) error {
	if role == core.RoleDeveloper {
		return fmt.Errorf("cannot branch at a developer instruction; choose a user or assistant message")
	}
	return nil
}

func branchRequestLocked(ctx context.Context, manager *Manager, anchorPath, anchorID string, recorded core.Role, needRefs bool) (branchRequest, error) {
	refs, role, system, err := branchLineageLocked(manager, anchorPath, anchorID, recorded)
	if err != nil {
		return branchRequest{}, err
	}
	request := branchRequest{carried: storedRefs(refs), system: system, path: anchorPath, anchorID: anchorID, anchorRole: role, recorded: recorded}
	if request.messages, err = buildTypedMessagesFromLineageRefs(ctx, refs); err != nil {
		return branchRequest{}, err
	}
	if request.head, err = pinHeadLocked(refs); err != nil {
		return branchRequest{}, err
	}
	if needRefs {
		request.refs = contextRefs(refs, request.messages)
	}
	return request, nil
}

// branchLineageLocked returns the lineage a request that branches at the
// anchor carries, the anchor's role, and the system prompt of the anchor's
// lineage. An assistant anchor is regenerated; a user anchor replaces its
// turn. A recorded role stands in for the anchor's current role.
func branchLineageLocked(manager *Manager, anchorPath, anchorID string, recorded core.Role) ([]lineageMessageRef, core.Role, *string, error) {
	refs, err := lineageMessageRefsWithManager(manager, anchorPath)
	if err != nil {
		return nil, "", nil, err
	}
	system := lineageSystemPrompt(refs)
	// The anchor directory's messages end the lineage. A deleted anchor
	// stands before the first of them with a later ID, and only a recorded
	// role says how a branch continues there.
	own := len(refs)
	for own > 0 && refs[own-1].path == anchorPath {
		own--
	}
	anchorIdx := own + replacedPosition(refs[own:], anchorID)
	role := recorded
	if anchorIdx < len(refs) && refs[anchorIdx].message.ID == anchorID {
		if role == "" {
			role = refs[anchorIdx].message.Role
		}
	} else if role == "" {
		return nil, "", nil, errors.WrapError("find branch anchor", fmt.Errorf("message %s was not found in lineage for %s", anchorID, anchorPath))
	}
	if err := validateBranchMessageRole(role); err != nil {
		return nil, "", nil, err
	}
	switch role {
	case core.RoleAssistant:
		return refs[:anchorIdx], role, system, nil
	case core.RoleUser:
		prevAssistantIdx := -1
		for i := anchorIdx - 1; i >= 0; i-- {
			if refs[i].message.Role == core.RoleAssistant {
				prevAssistantIdx = i
				break
			}
		}
		kept, err := userBranchRefs(refs[:anchorIdx], prevAssistantIdx)
		return kept, role, system, err
	default:
		return nil, "", nil, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", role, anchorID))
	}
}

// plannedBranchLocked reads again the lineage a request that branches at its
// anchor carries. It fails when that lineage holds other messages than the
// request carried, as when the anchor or a message before it was replaced.
func plannedBranchLocked(manager *Manager, request branchRequest) ([]lineageMessageRef, error) {
	refs, _, _, err := branchLineageLocked(manager, request.path, request.anchorID, request.recorded)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(storedRefs(refs), request.carried) {
		return nil, &HeadReplacedError{Expected: MessageRef{Path: request.path, ID: request.anchorID}}
	}
	return refs, nil
}

// storedRefs names each message of refs by its directory, ID and revision.
func storedRefs(refs []lineageMessageRef) []MessageRef {
	stored := make([]MessageRef, len(refs))
	for i, ref := range refs {
		stored[i] = MessageRef{Path: ref.path, ID: ref.message.ID, Revision: ref.message.Revision}
	}
	return stored
}

// systemPrefix returns the system message refs begin with, alone, or nothing
// when they begin with another message.
func systemPrefix(refs []lineageMessageRef) []lineageMessageRef {
	if len(refs) > 0 && refs[0].message.Role == core.RoleSystem {
		return refs[:1]
	}
	return nil
}

// ForkSessionThroughMessageWithManager creates a new session containing only
// the visible lineage through a specific committed message snapshot. The
// fork begins with newSystemPrompt as its system message, an empty prompt
// included, and with none when newSystemPrompt is nil.
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
		return forkSource{refs: refs, system: newSystemPrompt}, nil
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
		// A deleted branch point leaves its siblings in place. A sibling
		// that recorded its anchor role continues the messages before the
		// deleted one's ID.
		branchIdx := replacedPosition(refs, branchMsgID)
		var branchMsg *lineageMessageRef
		var current core.Role
		if branchIdx < len(refs) && refs[branchIdx].message.ID == branchMsgID {
			found := refs[branchIdx]
			branchMsg, current = &found, found.message.Role
		}
		role, err := siblingAnchorRole(filepath.Join(dir, comp), current)
		if err != nil {
			return lineageScan{}, err
		}
		if role == "" {
			return lineageScan{}, errors.WrapError("find branch point", fmt.Errorf("branch point %s not found in %s", branchMsgID, dir))
		}

		switch role {
		case core.RoleAssistant:
			lineage = append(lineage, refs[:branchIdx]...)
			lastAssistant = branchMsg
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
				kept, err := userBranchRefs(refs[:branchIdx], prevAssistIdx)
				if err != nil {
					return lineageScan{}, err
				}
				lineage = append(lineage, kept...)
				prevAssistant := refs[prevAssistIdx]
				lastAssistant = &prevAssistant
				assistantAlreadyInLineage = true
			} else {
				if lastAssistant != nil && !assistantAlreadyInLineage {
					lineage = append(lineage, *lastAssistant)
					assistantAlreadyInLineage = true
				}
				kept, err := userBranchRefs(refs[:branchIdx], -1)
				if err != nil {
					return lineageScan{}, err
				}
				lineage = append(lineage, kept...)
			}

		default:
			return lineageScan{}, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", role, branchMsgID))
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
	hasContext := map[string]bool{}
	toolNames := map[string]string{}
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

		original := core.TypedMessage{Role: string(msg.Role), Blocks: blocks}
		if !ok {
			original = buildTypedMessage(msg, toolInteraction, toolNames)
		} else {
			original.Blocks = applyToolNameIndex(slices.Clone(blocks), toolNames)
		}
		newMsg := Message{
			Role:             msg.Role,
			Content:          msg.Content,
			ThoughtSignature: msg.ThoughtSignature,
			Timestamp:        msg.Timestamp,
			Model:            msg.Model,
			Origin:           sourceRef(ref, original).Source,
			Window:           msg.Window,
			Pin:              msg.Pin,
			Incomplete:       msg.Incomplete,
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
		exists, checked := hasContext[ref.path]
		if !checked {
			_, err := os.Stat(filepath.Join(ref.path, checkpointDirectory))
			if err != nil && !os.IsNotExist(err) {
				return MessageRef{}, err
			}
			exists = err == nil
			hasContext[ref.path] = exists
		}
		if exists {
			if err = copyCheckpointFiles(ContextRef{MessageRef: MessageRef{Path: ref.path, ID: msg.ID}}, fork.Path, newMsgID); err != nil {
				return MessageRef{}, err
			}
		}
		id++
		logger.From(ctx).Debugf("Copied message %s -> %s (role=%s, hasTools=%v)", msg.ID, newMsgID, msg.Role, toolInteraction != nil)
	}
	return head, nil
}
