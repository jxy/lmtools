package proxy

import (
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/session"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const responsesStateVersion = 1

var (
	errResponsesStateDeleted   = stdErrors.New("responses state record is deleted")
	errResponsesStateNotActive = stdErrors.New("responses state record is no longer active")
)

type responsesState struct {
	root             string
	responsesDir     string
	conversationsDir string
	manager          *session.Manager
	mu               sync.Mutex
}

type responseRecord struct {
	Version            int                    `json:"version"`
	ID                 string                 `json:"id"`
	Object             string                 `json:"object"`
	Status             string                 `json:"status"`
	Model              string                 `json:"model"`
	ConversationID     string                 `json:"conversation_id,omitempty"`
	PreviousResponseID string                 `json:"previous_response_id,omitempty"`
	SessionPath        string                 `json:"session_path,omitempty"`
	MessageID          string                 `json:"message_id,omitempty"`
	CreatedAt          int64                  `json:"created_at"`
	CompletedAt        int64                  `json:"completed_at,omitempty"`
	Background         bool                   `json:"background,omitempty"`
	Stream             bool                   `json:"stream,omitempty"`
	Store              bool                   `json:"store"`
	Deleted            bool                   `json:"deleted,omitempty"`
	Instructions       string                 `json:"instructions,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
	Request            json.RawMessage        `json:"request,omitempty"`
	Raw                json.RawMessage        `json:"raw,omitempty"`
	Error              interface{}            `json:"error,omitempty"`
	IncompleteDetails  interface{}            `json:"incomplete_details,omitempty"`
}

type conversationRecord struct {
	Version        int                    `json:"version"`
	ID             string                 `json:"id"`
	Object         string                 `json:"object"`
	SessionPath    string                 `json:"session_path"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
	Deleted        bool                   `json:"deleted,omitempty"`
	DeletedItemIDs []string               `json:"deleted_item_ids,omitempty"`
	Instructions   string                 `json:"instructions,omitempty"`
	LastResponseID string                 `json:"last_response_id,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

func newResponsesState(configuredRoot string) *responsesState {
	root := configuredRoot
	if strings.TrimSpace(root) == "" {
		root = defaultProxySessionsDir()
	}
	return &responsesState{
		root:             root,
		responsesDir:     filepath.Join(root, "responses"),
		conversationsDir: filepath.Join(root, "conversations"),
		manager:          session.NewManager(root),
	}
}

func defaultProxySessionsDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".apiproxy", "sessions")
	}
	return filepath.Join(homeDir, ".apiproxy", "sessions")
}

func (s *responsesState) ensure() error {
	if s == nil {
		return fmt.Errorf("responses state is not configured")
	}
	for _, dir := range []string{s.root, s.responsesDir, s.conversationsDir} {
		if err := os.MkdirAll(dir, constants.DirPerm); err != nil {
			return errors.WrapError("create responses state directory", err)
		}
	}
	return nil
}

func (s *responsesState) createSession() (*session.Session, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	return s.manager.CreateSession("", nil)
}

func (s *responsesState) loadSession(sessionPath string) (*session.Session, error) {
	if sessionPath == "" {
		return nil, fmt.Errorf("session path is empty")
	}
	return s.manager.LoadSession(sessionPath)
}

func (s *responsesState) createConversation(metadata map[string]interface{}, instructions string) (*conversationRecord, *session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.createSession()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().Unix()
	rec := &conversationRecord{
		Version:      responsesStateVersion,
		ID:           generateUUID("conv_"),
		Object:       "conversation",
		SessionPath:  sess.Path,
		CreatedAt:    now,
		UpdatedAt:    now,
		Instructions: instructions,
		Metadata:     cloneStringInterfaceMap(metadata),
	}
	if err := s.saveConversationLocked(rec); err != nil {
		return nil, nil, err
	}
	return rec, sess, nil
}

func (s *responsesState) loadConversation(id string) (*conversationRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	var rec conversationRecord
	ok, err := s.readJSON(s.conversationPath(id), &rec)
	if err != nil || !ok {
		return nil, ok, err
	}
	if rec.Deleted {
		return nil, false, nil
	}
	return &rec, true, nil
}

func (s *responsesState) saveConversation(rec *conversationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveConversationLocked(rec)
}

func (s *responsesState) saveConversationLocked(rec *conversationRecord) error {
	if rec == nil {
		return fmt.Errorf("conversation record is nil")
	}
	if err := validateStateID(rec.ID); err != nil {
		return err
	}
	if !rec.Deleted {
		var existing conversationRecord
		ok, err := s.readJSON(s.conversationPath(rec.ID), &existing)
		if err != nil {
			return err
		}
		if ok && existing.Deleted {
			return errResponsesStateDeleted
		}
	}
	if rec.Version == 0 {
		rec.Version = responsesStateVersion
	}
	if rec.Object == "" {
		rec.Object = "conversation"
	}
	rec.UpdatedAt = time.Now().Unix()
	return s.writeJSON(s.conversationPath(rec.ID), rec)
}

func (s *responsesState) loadResponse(id string) (*responseRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	var rec responseRecord
	ok, err := s.readJSON(s.responsePath(id), &rec)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &rec, true, nil
}

func (s *responsesState) saveResponse(rec *responseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveResponseLocked(rec)
}

func (s *responsesState) saveResponseLocked(rec *responseRecord) error {
	if rec == nil {
		return fmt.Errorf("response record is nil")
	}
	if err := validateStateID(rec.ID); err != nil {
		return err
	}
	if !rec.Deleted {
		var existing responseRecord
		ok, err := s.readJSON(s.responsePath(rec.ID), &existing)
		if err != nil {
			return err
		}
		if ok && existing.Deleted {
			return errResponsesStateDeleted
		}
	}
	if rec.Version == 0 {
		rec.Version = responsesStateVersion
	}
	if rec.Object == "" {
		rec.Object = "response"
	}
	return s.writeJSON(s.responsePath(rec.ID), rec)
}

func (s *responsesState) cancelResponseIfPending(id string, errPayload interface{}) (*responseRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec responseRecord
	ok, err := s.readJSON(s.responsePath(id), &rec)
	if err != nil || !ok {
		return nil, ok, err
	}
	if rec.Deleted || !responseRecordPending(rec.Status) {
		return &rec, true, nil
	}
	rec.Status = "cancelled"
	rec.CompletedAt = time.Now().Unix()
	rec.Error = errPayload
	raw, err := marshalJSONRaw(responseRecordPayload(&rec))
	if err != nil {
		return nil, false, err
	}
	rec.Raw = raw
	if err := s.saveResponseLocked(&rec); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

func (s *responsesState) updateResponseStatusIfPending(id, status string, errPayload interface{}) error {
	if err := validateStateID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec responseRecord
	ok, err := s.readJSON(s.responsePath(id), &rec)
	if err != nil || !ok {
		return err
	}
	if rec.Deleted {
		return errResponsesStateDeleted
	}
	if !responseRecordPending(rec.Status) {
		return errResponsesStateNotActive
	}
	rec.Status = status
	rec.Error = errPayload
	if status == "failed" || status == "cancelled" {
		rec.CompletedAt = time.Now().Unix()
	}
	raw, err := marshalJSONRaw(responseRecordPayload(&rec))
	if err != nil {
		return err
	}
	rec.Raw = raw
	return s.saveResponseLocked(&rec)
}

func responseRecordPending(status string) bool {
	switch status {
	case "", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func (s *responsesState) deleteResponse(id string) (*responseRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec responseRecord
	ok, err := s.readJSON(s.responsePath(id), &rec)
	if err != nil || !ok {
		return nil, ok, err
	}
	rec.Deleted = true
	if err := s.saveResponseLocked(&rec); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

func (s *responsesState) deleteConversation(id string) (*conversationRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec conversationRecord
	ok, err := s.readJSON(s.conversationPath(id), &rec)
	if err != nil || !ok {
		return nil, ok, err
	}
	rec.Deleted = true
	rec.UpdatedAt = time.Now().Unix()
	if err := s.saveConversationLocked(&rec); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

func (s *responsesState) responsePath(id string) string {
	return filepath.Join(s.responsesDir, id+".json")
}

func (s *responsesState) conversationPath(id string) string {
	return filepath.Join(s.conversationsDir, id+".json")
}

func (s *responsesState) readJSON(path string, dst interface{}) (bool, error) {
	if err := s.ensure(); err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if stdErrors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.WrapError("read responses state", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, errors.WrapError("parse responses state", err)
	}
	return true, nil
}

func (s *responsesState) writeJSON(path string, value interface{}) error {
	if err := s.ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.WrapError("marshal responses state", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return errors.WrapError("create responses state temp file", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.WrapError("write responses state temp file", err)
	}
	if err := tmp.Close(); err != nil {
		return errors.WrapError("close responses state temp file", err)
	}
	if err := os.Chmod(tmpName, constants.FilePerm); err != nil {
		return errors.WrapError("chmod responses state temp file", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.WrapError("commit responses state", err)
	}
	return nil
}

func validateStateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("invalid id")
	}
	return nil
}

func typedMessageToolProjection(msg core.TypedMessage) (string, []core.ToolCall, []core.ToolResult) {
	textParts := make([]string, 0)
	calls := make([]core.ToolCall, 0)
	results := make([]core.ToolResult, 0)
	for _, block := range msg.Blocks {
		switch value := block.(type) {
		case core.TextBlock:
			if value.Text != "" {
				textParts = append(textParts, value.Text)
			}
		case core.ToolUseBlock:
			calls = append(calls, core.ToolCall{
				ID:           value.ID,
				Type:         value.Type,
				Namespace:    value.Namespace,
				OriginalName: value.OriginalName,
				Name:         value.Name,
				Args:         append(json.RawMessage(nil), value.Input...),
				Input:        value.InputString,
			})
		case core.ToolResultBlock:
			result := core.ToolResult{
				ID:     value.ToolUseID,
				Output: value.Content,
			}
			if value.IsError {
				result.Error = value.Content
			}
			results = append(results, result)
		}
	}
	return strings.Join(textParts, "\n"), calls, results
}
