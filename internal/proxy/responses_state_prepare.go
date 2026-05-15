package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"strings"
	"time"
)

type openAIResponsesStateContext struct {
	Session          *session.Session
	Conversation     *conversationRecord
	Previous         *responseRecord
	History          []core.TypedMessage
	Store            bool
	Background       bool
	Instructions     string
	CurrentRequest   json.RawMessage
	ExistingRecordID string
	ConversationBase conversationBaseRef
}

type conversationBaseRef struct {
	SessionPath       string
	LastResponseID    string
	MessagePath       string
	TerminalMessageID string
}

type openAIResponsesStateMode int

const (
	responsesStateReadOnly openAIResponsesStateMode = iota
	responsesStateForeground
	responsesStateBackground
)

func responsesStoreRequested(req *OpenAIResponsesRequest) bool {
	return req == nil || req.Store == nil || *req.Store
}

func (s *Server) prepareOpenAIResponsesStateReadOnly(ctx context.Context, req *OpenAIResponsesRequest, typed TypedRequest, originalModel string) (*openAIResponsesStateContext, TypedRequest, error) {
	return s.prepareOpenAIResponsesStateWithMode(ctx, req, typed, originalModel, responsesStateReadOnly, false)
}

func (s *Server) prepareOpenAIResponsesStateWithMode(ctx context.Context, req *OpenAIResponsesRequest, typed TypedRequest, originalModel string, mode openAIResponsesStateMode, store bool) (*openAIResponsesStateContext, TypedRequest, error) {
	convSpec, err := parseConversationSpec(req.Conversation)
	if err != nil {
		return nil, typed, err
	}
	readOnly := false
	background := false
	writable := false
	switch mode {
	case responsesStateReadOnly:
		readOnly = true
	case responsesStateForeground:
		writable = store
	case responsesStateBackground:
		background = true
		writable = true
	default:
		return nil, typed, fmt.Errorf("unknown responses state mode %d", mode)
	}
	if readOnly && convSpec.create {
		return nil, typed, fmt.Errorf("conversation id is required")
	}
	if !readOnly {
		needsState := writable || req.PreviousResponseID != "" || (convSpec.requested && !convSpec.create)
		if !needsState {
			return nil, typed, nil
		}
	}

	currentRequest, err := marshalJSONRaw(req)
	if err != nil {
		return nil, typed, err
	}
	stateCtx := &openAIResponsesStateContext{
		Store:          store,
		Background:     background,
		Instructions:   responsesInstructionText(req.Instructions),
		CurrentRequest: currentRequest,
	}
	if stateCtx.Instructions == "" {
		stateCtx.Instructions = typed.Developer
	}

	var sessionPath string
	if req.PreviousResponseID != "" {
		prev, err := s.loadCompletedPreviousResponse(req.PreviousResponseID)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Previous = prev
		sessionPath = prev.SessionPath
		if stateCtx.Instructions == "" {
			stateCtx.Instructions = prev.Instructions
		}
	}

	if convSpec.requested && (readOnly || !convSpec.create || writable) {
		conv, sess, err := s.prepareOpenAIResponsesConversationState(stateCtx, convSpec, writable)
		if err != nil {
			return nil, typed, err
		}
		if stateCtx.Previous == nil {
			sessionPath = conv.SessionPath
			stateCtx.Session = sess
		}
		stateCtx.Conversation = conv
		if !readOnly {
			if err := s.captureOpenAIResponsesConversationBase(stateCtx, conv); err != nil {
				return nil, typed, err
			}
		}
		if stateCtx.Instructions == "" {
			stateCtx.Instructions = conv.Instructions
		}
	}

	if stateCtx.Previous != nil && writable {
		forkedSession, err := s.forkOpenAIResponsesResponseSnapshot(ctx, stateCtx.Previous)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Session = forkedSession
		sessionPath = forkedSession.Path
	}

	if sessionPath == "" && writable {
		sess, err := s.responsesState.createSession()
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Session = sess
	} else if sessionPath != "" && stateCtx.Session == nil {
		sess, err := s.responsesState.loadSession(sessionPath)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.Session = sess
	}

	if stateCtx.Session != nil {
		typed, err = s.prependOpenAIResponsesStateHistory(ctx, stateCtx, typed, readOnly, writable)
		if err != nil {
			return nil, typed, err
		}
	}
	if typed.Developer == "" {
		typed.Developer = stateCtx.Instructions
	}
	if originalModel != "" && typed.Metadata == nil {
		typed.Metadata = map[string]interface{}{}
	}
	return stateCtx, typed, nil
}

func (s *Server) prepareOpenAIResponsesConversationState(stateCtx *openAIResponsesStateContext, convSpec conversationSpec, writable bool) (*conversationRecord, *session.Session, error) {
	var conv *conversationRecord
	var sess *session.Session
	var err error
	if convSpec.create {
		if stateCtx.Previous != nil {
			return nil, nil, fmt.Errorf("conversation does not match previous_response_id")
		}
		conv, sess, err = s.responsesState.createConversation(convSpec.metadata, stateCtx.Instructions)
		if err != nil {
			return nil, nil, err
		}
	} else {
		var ok bool
		conv, ok, err = s.responsesState.loadConversation(convSpec.id)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, fmt.Errorf("conversation %q was not found", convSpec.id)
		}
		if writable && len(convSpec.metadata) > 0 {
			conv.Metadata = cloneStringInterfaceMap(convSpec.metadata)
		}
	}
	if stateCtx.Previous != nil && stateCtx.Previous.ConversationID != conv.ID {
		return nil, nil, fmt.Errorf("conversation does not match previous_response_id")
	}
	return conv, sess, nil
}

func (s *Server) prependOpenAIResponsesStateHistory(ctx context.Context, stateCtx *openAIResponsesStateContext, typed TypedRequest, readOnly bool, writable bool) (TypedRequest, error) {
	var history []core.TypedMessage
	var err error
	if stateCtx.Previous != nil && (!writable || readOnly) {
		history, err = s.responseRecordHistory(ctx, stateCtx.Previous)
	} else if stateCtx.Conversation != nil && stateCtx.Previous == nil {
		history, err = s.conversationHistory(ctx, stateCtx.Conversation)
	} else {
		history, err = session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, stateCtx.Session.Path)
	}
	if err != nil {
		return typed, err
	}
	stateCtx.History = history
	typed.Messages = append(append([]core.TypedMessage{}, history...), typed.Messages...)
	return typed, nil
}

func convertedOpenAIResponsesCompaction(stateCtx *openAIResponsesStateContext, typedWithState TypedRequest, summary string, upstreamResp *AnthropicResponse) *OpenAIResponsesCompactionResponse {
	now := time.Now().Unix()
	return &OpenAIResponsesCompactionResponse{
		ID:        generateUUID("resp_"),
		Object:    "response.compaction",
		CreatedAt: now,
		Output:    compactedResponseOutputItems(stateCtx, typedWithState.Messages, summary),
		Usage:     anthropicUsageToResponsesUsage(upstreamResp),
	}
}

type openAIResponsesCommitTarget struct {
	Session             *session.Session
	Conversation        *conversationRecord
	AdvanceConversation bool
	ExistingCreatedAt   int64
}

func (s *Server) commitOpenAIResponsesStateWithBlocks(ctx context.Context, stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, typedCurrent TypedRequest, resp *OpenAIResponsesResponse, originalModel string, assistantBlocks []core.Block) error {
	if resp == nil {
		return nil
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		attachOpenAIResponsesConversation(resp, stateCtx.Conversation.ID)
	}
	if stateCtx == nil || stateCtx.Session == nil {
		return nil
	}
	if !stateCtx.Store && !stateCtx.Background {
		return nil
	}
	if req == nil {
		req = &OpenAIResponsesRequest{Model: originalModel}
	}
	if stateCtx.ExistingRecordID != "" {
		resp.ID = stateCtx.ExistingRecordID
	}

	s.responsesState.mu.Lock()
	defer s.responsesState.mu.Unlock()

	target, err := s.prepareOpenAIResponsesCommitTargetLocked(ctx, stateCtx, resp.ID)
	if err != nil {
		return err
	}
	if target.ExistingCreatedAt != 0 {
		resp.CreatedAt = target.ExistingCreatedAt
	}
	raw, err := marshalJSONRaw(resp)
	if err != nil {
		return err
	}

	result, err := appendOpenAIResponsesCommitMessages(ctx, target.Session, typedCurrent, resp, originalModel, assistantBlocks)
	if err != nil {
		return err
	}
	target.Session.Path = result.Path
	stateCtx.Session = target.Session

	return s.saveOpenAIResponsesCommitRecordsLocked(stateCtx, req, resp, originalModel, result, target, raw)
}

func (s *Server) prepareOpenAIResponsesCommitTargetLocked(ctx context.Context, stateCtx *openAIResponsesStateContext, responseID string) (openAIResponsesCommitTarget, error) {
	if err := s.ensureOpenAIResponsesStateWritableLocked(stateCtx, responseID, true); err != nil {
		return openAIResponsesCommitTarget{}, err
	}
	target := openAIResponsesCommitTarget{Session: stateCtx.Session}
	existingCreatedAt := int64(0)
	if stateCtx.ExistingRecordID != "" {
		var existing responseRecord
		ok, err := s.responsesState.readJSON(s.responsesState.responsePath(stateCtx.ExistingRecordID), &existing)
		if err != nil {
			return openAIResponsesCommitTarget{}, err
		}
		if ok {
			existingCreatedAt = existing.CreatedAt
		}
	}
	target.ExistingCreatedAt = existingCreatedAt
	if stateCtx.Conversation != nil {
		conv, matches, err := s.openAIResponsesConversationHeadMatchesLocked(stateCtx)
		if err != nil {
			return openAIResponsesCommitTarget{}, err
		}
		target.Conversation = conv
		if matches {
			target.AdvanceConversation = true
		} else {
			forkedSession, err := s.forkOpenAIResponsesConversationBase(ctx, stateCtx)
			if err != nil {
				return openAIResponsesCommitTarget{}, err
			}
			target.Session = forkedSession
		}
	}
	return target, nil
}

func appendOpenAIResponsesCommitMessages(ctx context.Context, commitSession *session.Session, typedCurrent TypedRequest, resp *OpenAIResponsesResponse, originalModel string, assistantBlocks []core.Block) (session.SaveResult, error) {
	for _, msg := range typedCurrent.Messages {
		if err := appendTypedMessageToSession(ctx, commitSession, msg, originalModel); err != nil {
			return session.SaveResult{}, err
		}
	}
	assistantResponse := openAIResponsesCoreResponse(resp)
	if len(assistantBlocks) > 0 {
		assistantResponse.Blocks = assistantBlocks
	}
	result, err := session.SaveAssistantResponse(ctx, commitSession, assistantResponse, originalModel)
	if err != nil {
		return session.SaveResult{}, err
	}
	return result, nil
}

func (s *Server) saveOpenAIResponsesCommitRecordsLocked(stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, resp *OpenAIResponsesResponse, originalModel string, result session.SaveResult, target openAIResponsesCommitTarget, raw json.RawMessage) error {
	rec := &responseRecord{
		Version:            responsesStateVersion,
		ID:                 resp.ID,
		Object:             "response",
		Status:             firstNonEmpty(resp.Status, "completed"),
		Model:              originalModel,
		PreviousResponseID: req.PreviousResponseID,
		SessionPath:        stateCtx.Session.Path,
		MessageID:          result.MessageID,
		CreatedAt:          resp.CreatedAt,
		CompletedAt:        time.Now().Unix(),
		Background:         stateCtx.Background,
		Stream:             req.Stream,
		Store:              stateCtx.Store,
		Instructions:       stateCtx.Instructions,
		Metadata:           cloneStringInterfaceMap(req.Metadata),
		Request:            append(json.RawMessage(nil), stateCtx.CurrentRequest...),
		Raw:                raw,
		Error:              resp.Error,
		IncompleteDetails:  resp.IncompleteDetails,
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = rec.CompletedAt
	}
	if target.Conversation != nil {
		rec.ConversationID = target.Conversation.ID
		if target.AdvanceConversation {
			target.Conversation.SessionPath = target.Session.Path
			target.Conversation.LastResponseID = resp.ID
			if stateCtx.Conversation.Metadata != nil {
				target.Conversation.Metadata = cloneStringInterfaceMap(stateCtx.Conversation.Metadata)
			}
			if stateCtx.Instructions != "" {
				target.Conversation.Instructions = stateCtx.Instructions
			}
			if err := s.responsesState.saveConversationLocked(target.Conversation); err != nil {
				return err
			}
			stateCtx.Conversation = target.Conversation
		}
	}
	return s.responsesState.saveResponseLocked(rec)
}

func (s *Server) responseRecordHistory(ctx context.Context, rec *responseRecord) ([]core.TypedMessage, error) {
	if rec == nil {
		return nil, fmt.Errorf("response record is nil")
	}
	if rec.SessionPath == "" {
		return nil, fmt.Errorf("response session path is empty")
	}
	if rec.MessageID == "" {
		return session.BuildMessagesWithToolInteractionsWithManager(ctx, s.responsesState.manager, rec.SessionPath)
	}
	return session.BuildMessagesWithToolInteractionsThroughMessageWithManager(ctx, s.responsesState.manager, rec.SessionPath, rec.SessionPath, rec.MessageID)
}

func (s *Server) forkOpenAIResponsesResponseSnapshot(ctx context.Context, rec *responseRecord) (*session.Session, error) {
	if rec == nil {
		return nil, fmt.Errorf("response record is nil")
	}
	if rec.SessionPath == "" {
		return nil, fmt.Errorf("response session path is empty")
	}
	if rec.MessageID == "" {
		return session.ForkSessionWithManager(ctx, s.responsesState.manager, rec.SessionPath, nil)
	}
	return session.ForkSessionThroughMessageWithManager(ctx, s.responsesState.manager, rec.SessionPath, rec.SessionPath, rec.MessageID, nil)
}

func (s *Server) captureOpenAIResponsesConversationBase(stateCtx *openAIResponsesStateContext, conv *conversationRecord) error {
	if stateCtx == nil || conv == nil {
		return nil
	}
	if stateCtx.Previous != nil {
		stateCtx.ConversationBase = conversationBaseRef{
			SessionPath:       stateCtx.Previous.SessionPath,
			LastResponseID:    stateCtx.Previous.ID,
			MessagePath:       stateCtx.Previous.SessionPath,
			TerminalMessageID: stateCtx.Previous.MessageID,
		}
		if stateCtx.ConversationBase.TerminalMessageID == "" {
			ref, ok, err := session.LastMessageRefWithManager(s.responsesState.manager, stateCtx.Previous.SessionPath)
			if err != nil {
				return err
			}
			if ok {
				stateCtx.ConversationBase.MessagePath = ref.Path
				stateCtx.ConversationBase.TerminalMessageID = ref.ID
			}
		}
		return nil
	}

	stateCtx.ConversationBase = conversationBaseRef{
		SessionPath:    conv.SessionPath,
		LastResponseID: conv.LastResponseID,
	}
	ref, ok, err := session.LastMessageRefWithManager(s.responsesState.manager, conv.SessionPath)
	if err != nil {
		return err
	}
	if ok {
		stateCtx.ConversationBase.MessagePath = ref.Path
		stateCtx.ConversationBase.TerminalMessageID = ref.ID
	}
	return nil
}

func (s *Server) openAIResponsesConversationHeadMatchesLocked(stateCtx *openAIResponsesStateContext) (*conversationRecord, bool, error) {
	if stateCtx == nil || stateCtx.Conversation == nil {
		return nil, false, fmt.Errorf("conversation state is missing")
	}
	var conv conversationRecord
	ok, err := s.responsesState.readJSON(s.responsesState.conversationPath(stateCtx.Conversation.ID), &conv)
	if err != nil {
		return nil, false, err
	}
	if !ok || conv.Deleted {
		return nil, false, errResponsesStateDeleted
	}
	base := stateCtx.ConversationBase
	if conv.SessionPath != base.SessionPath || conv.LastResponseID != base.LastResponseID {
		return &conv, false, nil
	}

	ref, ok, err := session.LastMessageRefWithManager(s.responsesState.manager, conv.SessionPath)
	if err != nil {
		return nil, false, err
	}
	if base.TerminalMessageID == "" {
		return &conv, !ok, nil
	}
	return &conv, ok && ref.Path == base.MessagePath && ref.ID == base.TerminalMessageID, nil
}

func (s *Server) forkOpenAIResponsesConversationBase(ctx context.Context, stateCtx *openAIResponsesStateContext) (*session.Session, error) {
	if stateCtx != nil && stateCtx.Previous != nil && stateCtx.Session != nil {
		return stateCtx.Session, nil
	}
	if stateCtx == nil || stateCtx.ConversationBase.SessionPath == "" {
		return nil, fmt.Errorf("conversation base session path is empty")
	}
	if stateCtx.ConversationBase.TerminalMessageID == "" {
		return s.responsesState.createSession()
	}
	return session.ForkSessionThroughMessageWithManager(
		ctx,
		s.responsesState.manager,
		stateCtx.ConversationBase.SessionPath,
		stateCtx.ConversationBase.MessagePath,
		stateCtx.ConversationBase.TerminalMessageID,
		nil,
	)
}

func (s *Server) ensureOpenAIResponsesStateWritableLocked(stateCtx *openAIResponsesStateContext, responseID string, requireActive bool) error {
	if responseID != "" {
		var rec responseRecord
		ok, err := s.responsesState.readJSON(s.responsesState.responsePath(responseID), &rec)
		if err != nil {
			return err
		}
		if ok && rec.Deleted {
			return errResponsesStateDeleted
		}
		if ok && requireActive && !responseRecordPending(rec.Status) {
			return errResponsesStateNotActive
		}
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		var conv conversationRecord
		ok, err := s.responsesState.readJSON(s.responsesState.conversationPath(stateCtx.Conversation.ID), &conv)
		if err != nil {
			return err
		}
		if !ok || conv.Deleted {
			return errResponsesStateDeleted
		}
	}
	return nil
}

func appendTypedMessageToSession(ctx context.Context, sess *session.Session, msg core.TypedMessage, model string) error {
	text, calls, results := typedMessageToolProjection(msg)
	role := core.Role(msg.Role)
	if role == "" {
		role = core.RoleUser
	}
	message := session.Message{
		Role:      role,
		Content:   text,
		Timestamp: time.Now(),
	}
	if role == core.RoleAssistant {
		message.Model = model
	}
	result, err := session.AppendMessageWithBlocks(ctx, sess, message, calls, results, msg.Blocks)
	if err != nil {
		return err
	}
	sess.Path = result.Path
	return nil
}

func parseConversationSpec(raw interface{}) (conversationSpec, error) {
	if raw == nil {
		return conversationSpec{}, nil
	}
	switch value := raw.(type) {
	case string:
		switch strings.TrimSpace(value) {
		case "", "none":
			return conversationSpec{}, nil
		case "auto":
			return conversationSpec{requested: true, create: true}, nil
		default:
			return conversationSpec{requested: true, id: value}, nil
		}
	case map[string]interface{}:
		spec := conversationSpec{requested: true}
		if id, _ := value["id"].(string); id != "" {
			spec.id = id
		} else {
			spec.create = true
		}
		if metadata, _ := value["metadata"].(map[string]interface{}); metadata != nil {
			spec.metadata = metadata
		}
		return spec, nil
	default:
		return conversationSpec{}, fmt.Errorf("conversation must be a string or object")
	}
}

type conversationSpec struct {
	requested bool
	create    bool
	id        string
	metadata  map[string]interface{}
}
