package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"strings"
	"time"
)

// openAIResponsesStateContext is where a request's stored conversation
// stands: the session its messages go to, the message it continues after,
// and the conversation items its history leaves out.
type openAIResponsesStateContext struct {
	// Session is the session number; empty until the request has one.
	Session string
	// Base is the message the request continues after; empty for the
	// session root.
	Base         string
	Conversation *conversationRecord
	// ConversationGeneration is the conversation's generation when the
	// request was prepared.
	ConversationGeneration int64
	Previous               *responseRecord
	HiddenItems            []storedItem
	History                []core.TypedMessage
	Store                  bool
	Background             bool
	Instructions           string
	// RequestInstructions are the instructions the request itself gave,
	// which alone change its conversation's.
	RequestInstructions string
	CurrentRequest      json.RawMessage
	ExistingRecordID    string
}

type openAIResponsesStateMode int

const (
	responsesStateModeReadOnly openAIResponsesStateMode = iota
	responsesStateModeForegroundStored
	responsesStateModeForegroundTransient
	responsesStateModeBackground
)

func (m openAIResponsesStateMode) readOnly() bool {
	return m == responsesStateModeReadOnly
}

func (m openAIResponsesStateMode) writable() bool {
	return m == responsesStateModeForegroundStored || m == responsesStateModeBackground
}

func (m openAIResponsesStateMode) background() bool {
	return m == responsesStateModeBackground
}

func (m openAIResponsesStateMode) store(req *OpenAIResponsesRequest) bool {
	switch m {
	case responsesStateModeForegroundStored:
		return true
	case responsesStateModeBackground:
		return responsesStoreRequested(req)
	default:
		return false
	}
}

func responsesStoreRequested(req *OpenAIResponsesRequest) bool {
	return req == nil || req.Store == nil || *req.Store
}

func (s *Server) prepareOpenAIResponsesStateReadOnly(ctx context.Context, req *OpenAIResponsesRequest, typed TypedRequest) (*openAIResponsesStateContext, TypedRequest, error) {
	return s.prepareOpenAIResponsesState(ctx, req, nil, typed, responsesStateModeReadOnly)
}

// prepareOpenAIResponsesStateForeground records rawRequest as the stored request
// when the response is persisted. Passing the client's own bytes avoids
// re-marshalling a request that can be hundreds of megabytes; callers without
// them may pass nil.
func (s *Server) prepareOpenAIResponsesStateForeground(ctx context.Context, req *OpenAIResponsesRequest, rawRequest json.RawMessage, typed TypedRequest) (*openAIResponsesStateContext, TypedRequest, error) {
	if responsesStoreRequested(req) {
		return s.prepareOpenAIResponsesState(ctx, req, rawRequest, typed, responsesStateModeForegroundStored)
	}
	return s.prepareOpenAIResponsesState(ctx, req, rawRequest, typed, responsesStateModeForegroundTransient)
}

func (s *Server) prepareOpenAIResponsesStateBackground(ctx context.Context, req *OpenAIResponsesRequest, rawRequest json.RawMessage, typed TypedRequest) (*openAIResponsesStateContext, TypedRequest, error) {
	return s.prepareOpenAIResponsesState(ctx, req, rawRequest, typed, responsesStateModeBackground)
}

func (s *Server) prepareOpenAIResponsesState(ctx context.Context, req *OpenAIResponsesRequest, rawRequest json.RawMessage, typed TypedRequest, mode openAIResponsesStateMode) (*openAIResponsesStateContext, TypedRequest, error) {
	convSpec, err := parseConversationSpec(req.Conversation)
	if err != nil {
		return nil, typed, err
	}
	if mode.readOnly() && convSpec.create {
		return nil, typed, fmt.Errorf("conversation id is required")
	}
	if !mode.readOnly() {
		needsState := mode.writable() || req.PreviousResponseID != "" || (convSpec.requested && !convSpec.create)
		if !needsState {
			return nil, typed, nil
		}
	}

	stateCtx, err := newOpenAIResponsesStateContext(ctx, req, rawRequest, typed, mode)
	if err != nil {
		return nil, typed, err
	}
	if err := s.loadOpenAIResponsesPreviousState(req, stateCtx); err != nil {
		return nil, typed, err
	}
	if err := s.prepareOpenAIResponsesConversationForRequest(ctx, stateCtx, convSpec, mode); err != nil {
		return nil, typed, err
	}
	if stateCtx.Session == "" && mode.writable() {
		if stateCtx.Session, err = s.responsesState.createSession(ctx); err != nil {
			return nil, typed, err
		}
	}
	if stateCtx.Session != "" {
		history, err := s.responsesState.history(stateCtx.Session, stateCtx.Base, stateCtx.HiddenItems)
		if err != nil {
			return nil, typed, err
		}
		stateCtx.History = history
		typed.Messages = append(append([]core.TypedMessage{}, history...), typed.Messages...)
	}
	if typed.Developer == "" {
		typed.Developer = stateCtx.Instructions
	}
	return stateCtx, typed, nil
}

func newOpenAIResponsesStateContext(ctx context.Context, req *OpenAIResponsesRequest, rawRequest json.RawMessage, typed TypedRequest, mode openAIResponsesStateMode) (*openAIResponsesStateContext, error) {
	// Only writable modes persist the request, and the client's raw bytes are a
	// more faithful record than a re-marshal of the parsed struct. Re-marshal
	// only when a caller had no raw body to hand over.
	currentRequest := rawRequest
	if len(currentRequest) == 0 && mode.writable() {
		marshalled, err := marshalJSONRaw(req)
		if err != nil {
			return nil, err
		}
		currentRequest = marshalled
	}
	stateCtx := &openAIResponsesStateContext{
		Store:          mode.store(req),
		Background:     mode.background(),
		Instructions:   responsesInstructionText(ctx, req.Instructions),
		CurrentRequest: currentRequest,
	}
	if stateCtx.Instructions == "" {
		stateCtx.Instructions = typed.Developer
	}
	stateCtx.RequestInstructions = stateCtx.Instructions
	return stateCtx, nil
}

// loadOpenAIResponsesPreviousState makes a request with previous_response_id
// continue where that response ends, with the items it was prepared without.
func (s *Server) loadOpenAIResponsesPreviousState(req *OpenAIResponsesRequest, stateCtx *openAIResponsesStateContext) error {
	if req.PreviousResponseID == "" {
		return nil
	}
	prev, err := s.loadCompletedPreviousResponse(req.PreviousResponseID)
	if err != nil {
		return err
	}
	if prev.Session == "" {
		return fmt.Errorf("previous_response_id %q has no stored conversation", req.PreviousResponseID)
	}
	stateCtx.Previous = prev
	stateCtx.Session, stateCtx.Base = prev.Session, prev.MessageID
	stateCtx.HiddenItems = append([]storedItem(nil), prev.HiddenItems...)
	if stateCtx.Instructions == "" {
		stateCtx.Instructions = prev.Instructions
	}
	return nil
}

// prepareOpenAIResponsesConversationForRequest loads or creates the
// request's conversation and records its generation. Without
// previous_response_id the request continues after the conversation's last
// message. Either way it leaves out the items the conversation deleted.
func (s *Server) prepareOpenAIResponsesConversationForRequest(ctx context.Context, stateCtx *openAIResponsesStateContext, convSpec conversationSpec, mode openAIResponsesStateMode) error {
	if !convSpec.requested || (!mode.readOnly() && convSpec.create && !mode.writable()) {
		return nil
	}
	conv, err := s.prepareOpenAIResponsesConversationState(ctx, stateCtx, convSpec, mode.writable())
	if err != nil {
		return err
	}
	stateCtx.Conversation = conv
	stateCtx.ConversationGeneration = conv.Generation
	if stateCtx.Instructions == "" {
		stateCtx.Instructions = conv.Instructions
	}
	if stateCtx.Previous == nil {
		stateCtx.Session, stateCtx.Base = conv.Session, conv.Head
	}
	for _, item := range conv.DeletedItems {
		stateCtx.HiddenItems = appendUniqueItem(stateCtx.HiddenItems, item)
	}
	return nil
}

func (s *Server) prepareOpenAIResponsesConversationState(ctx context.Context, stateCtx *openAIResponsesStateContext, convSpec conversationSpec, writable bool) (*conversationRecord, error) {
	if convSpec.create {
		if stateCtx.Previous != nil {
			return nil, fmt.Errorf("conversation does not match previous_response_id")
		}
		return s.responsesState.createConversation(ctx, convSpec.metadata, stateCtx.Instructions, nil)
	}
	conv, ok, err := s.responsesState.loadConversation(convSpec.id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("conversation %q was not found", convSpec.id)
	}
	if writable && len(convSpec.metadata) > 0 {
		conv.Metadata = cloneStringInterfaceMap(convSpec.metadata)
	}
	if stateCtx.Previous != nil && stateCtx.Previous.ConversationID != conv.ID {
		return nil, fmt.Errorf("conversation does not match previous_response_id")
	}
	return conv, nil
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

// commitOpenAIResponsesStateWithBlocks saves a response's messages after the
// message its request continued, then publishes it: saves its record and
// advances the conversation when the request continued the conversation's
// last message and nothing changed the conversation meanwhile. A response
// that does not advance its conversation stays a branch at the place it was
// built on.
func (s *Server) commitOpenAIResponsesStateWithBlocks(ctx context.Context, stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, typedCurrent TypedRequest, resp *OpenAIResponsesResponse, originalModel string, assistantBlocks []core.Block) error {
	if resp == nil {
		return nil
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		attachOpenAIResponsesConversation(resp, stateCtx.Conversation.ID)
	}
	if stateCtx == nil || stateCtx.Session == "" {
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

	existing, ok, err := s.ensureOpenAIResponsesStateWritableLocked(stateCtx, resp.ID, true)
	if err != nil {
		return err
	}
	if ok && existing.CreatedAt != 0 {
		resp.CreatedAt = existing.CreatedAt
	}
	raw, err := marshalJSONRaw(resp)
	if err != nil {
		return err
	}

	messages := append([]core.TypedMessage(nil), typedCurrent.Messages...)
	reply := openAIResponsesCoreResponse(resp)
	if len(assistantBlocks) > 0 {
		reply.Blocks = assistantBlocks
	}
	// A reply with nothing in it, as a content filter leaves, adds no
	// assistant message. An empty one would go to the backend with the next
	// request, and backends refuse an assistant message with no content.
	if core.ResponseHasContent(reply) {
		messages = append(messages, core.TypedMessage{Role: string(core.RoleAssistant), Blocks: core.ReplyBlocks(reply)})
	}
	cutShort := resp.Status == "incomplete" || resp.Status == "failed"
	last, err := s.responsesState.appendMessages(ctx, stateCtx.Session, stateCtx.Base, messages, originalModel, cutShort)
	if err != nil {
		return err
	}
	if responseMessagesSavedForTest != nil {
		responseMessagesSavedForTest()
	}
	return s.publishOpenAIResponseLocked(ctx, stateCtx, req, resp, originalModel, last, raw)
}

// responseMessagesSavedForTest runs after a response's messages are saved
// and before the response is published.
var responseMessagesSavedForTest func()

// publishOpenAIResponseLocked saves the record of a response whose messages
// end at last and advances its conversation. It holds the conversation's
// lock and then the response's from checking that neither was deleted and
// the response was not cancelled to its last write, so a deletion or
// cancellation in another apiproxy comes before the publication, which then
// saves nothing, or after it. The caller holds the state lock.
func (s *Server) publishOpenAIResponseLocked(ctx context.Context, stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, resp *OpenAIResponsesResponse, originalModel, last string, raw json.RawMessage) error {
	publish := func() error {
		return s.responsesState.withResponseLock(ctx, resp.ID, func() error {
			if _, _, err := s.ensureOpenAIResponsesStateWritableLocked(stateCtx, resp.ID, true); err != nil {
				return err
			}
			if stateCtx.Conversation != nil && conversationChangeForTest != nil {
				conversationChangeForTest()
			}
			if responseChangeForTest != nil {
				responseChangeForTest()
			}
			if err := s.saveOpenAIResponsesRecordLocked(stateCtx, req, resp, originalModel, last, raw); err != nil {
				return err
			}
			return s.advanceOpenAIResponsesConversationLocked(stateCtx, resp.ID, last)
		})
	}
	if stateCtx.Conversation == nil {
		return publish()
	}
	return s.responsesState.withConversationLock(ctx, stateCtx.Conversation.ID, publish)
}

func (s *Server) saveOpenAIResponsesRecordLocked(stateCtx *openAIResponsesStateContext, req *OpenAIResponsesRequest, resp *OpenAIResponsesResponse, originalModel, last string, raw json.RawMessage) error {
	rec := &responseRecord{
		Version:            responsesStateVersion,
		ID:                 resp.ID,
		Object:             "response",
		Status:             firstNonEmpty(resp.Status, "completed"),
		Model:              originalModel,
		PreviousResponseID: req.PreviousResponseID,
		Session:            stateCtx.Session,
		MessageID:          last,
		HiddenItems:        stateCtx.HiddenItems,
		CreatedAt:          resp.CreatedAt,
		CompletedAt:        time.Now().Unix(),
		Background:         stateCtx.Background,
		Stream:             req.Stream,
		Store:              stateCtx.Store,
		Instructions:       stateCtx.Instructions,
		Metadata:           cloneStringInterfaceMap(req.Metadata),
		Request:            stateCtx.CurrentRequest,
		Raw:                raw,
		Error:              resp.Error,
		IncompleteDetails:  resp.IncompleteDetails,
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = rec.CompletedAt
	}
	if stateCtx.Conversation != nil {
		rec.ConversationID = stateCtx.Conversation.ID
	}
	// The publication read the record under its lock and found it neither
	// deleted nor cancelled.
	return s.responsesState.writeResponseLocked(rec.ID, rec)
}

// beforeConversationAdvanceForTest runs after a response's messages and
// record are saved and before its conversation advances. An error it returns
// stops the commit there, the way a crash would.
var beforeConversationAdvanceForTest func() error

// advanceOpenAIResponsesConversationLocked moves the conversation to last
// when the request continued the conversation's last message, and its last
// response when it named one, and the conversation's generation is the one
// the request was prepared with. A request that continued an older response,
// or ran while an item was deleted or the instructions changed, leaves the
// conversation where it is. Only instructions the request gave change the
// conversation's. The caller holds the state lock and the conversation's
// lock.
func (s *Server) advanceOpenAIResponsesConversationLocked(stateCtx *openAIResponsesStateContext, responseID, last string) error {
	if stateCtx.Conversation == nil {
		return nil
	}
	if beforeConversationAdvanceForTest != nil {
		if err := beforeConversationAdvanceForTest(); err != nil {
			return err
		}
	}
	conv, ok, err := s.responsesState.readConversation(stateCtx.Conversation.ID)
	if err != nil {
		return err
	}
	if !ok || conv.Deleted {
		return errResponsesConversationDeleted
	}
	if conv.Generation != stateCtx.ConversationGeneration || conv.Session != stateCtx.Session || conv.Head != stateCtx.Base {
		return nil
	}
	if stateCtx.Previous != nil && stateCtx.Previous.ID != conv.LastResponseID {
		return nil
	}
	conv.Head = last
	conv.LastResponseID = responseID
	if stateCtx.Conversation.Metadata != nil {
		conv.Metadata = cloneStringInterfaceMap(stateCtx.Conversation.Metadata)
	}
	if stateCtx.RequestInstructions != "" {
		conv.Instructions = stateCtx.RequestInstructions
	}
	if err := s.responsesState.saveConversationLocked(stateCtx.Conversation.ID, &conv); err != nil {
		return err
	}
	stateCtx.Conversation = &conv
	return nil
}

// ensureOpenAIResponsesStateWritableLocked checks that response responseID,
// when it has a record, is not deleted and, with requireActive, is still
// pending, that its file holds no record other than the response's own, and
// that the request's conversation is not deleted. It returns the response's
// record, which it read whole once.
func (s *Server) ensureOpenAIResponsesStateWritableLocked(stateCtx *openAIResponsesStateContext, responseID string, requireActive bool) (responseRecord, bool, error) {
	var rec responseRecord
	ok := false
	if responseID != "" {
		var err error
		if rec, ok, err = s.responsesState.readResponseFile(responseID); err != nil {
			return rec, false, err
		}
		if ok && !ownRecord(responseID, rec.ID, rec.Version) {
			return responseRecord{}, false, errResponsesStateOccupied
		}
		if ok && rec.Deleted {
			return rec, ok, errResponsesStateDeleted
		}
		if ok && requireActive && !responseRecordPending(rec.Status) {
			return rec, ok, errResponsesStateNotActive
		}
	}
	if stateCtx != nil && stateCtx.Conversation != nil {
		conv, found, err := s.responsesState.readConversation(stateCtx.Conversation.ID)
		if err != nil {
			return rec, ok, err
		}
		if !found || conv.Deleted {
			return rec, ok, errResponsesConversationDeleted
		}
	}
	return rec, ok, nil
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
