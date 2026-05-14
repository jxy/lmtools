package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const memorySessionPath = "memory"

// MemorySessionStore keeps tool-loop conversation state without writing session files.
type MemorySessionStore struct {
	mu       sync.Mutex
	path     string
	nextID   int
	messages []TypedMessage
	toolName map[string]string
}

// NewMemorySessionStore creates an in-memory store seeded with the request context.
func NewMemorySessionStore(system, userInput string) *MemorySessionStore {
	store := &MemorySessionStore{
		path:     memorySessionPath,
		toolName: make(map[string]string),
	}
	if system != "" {
		store.messages = append(store.messages, NewTextMessage(string(RoleSystem), system))
	}
	if userInput != "" {
		store.messages = append(store.messages, NewTextMessage(string(RoleUser), userInput))
	}
	return store
}

// Messages returns a copy of the current in-memory conversation.
func (s *MemorySessionStore) Messages(_ string) ([]TypedMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTypedMessages(s.messages), nil
}

func (s *MemorySessionStore) SaveAssistant(ctx context.Context, text string, calls []ToolCall, model string) (string, string, error) {
	return s.SaveAssistantWithThoughtSignature(ctx, text, calls, model, "")
}

func (s *MemorySessionStore) SaveAssistantWithThoughtSignature(ctx context.Context, text string, calls []ToolCall, _ string, thoughtSignature string) (string, string, error) {
	response := Response{
		Text:             text,
		ToolCalls:        calls,
		Blocks:           responseBlocksFromParts(strings.TrimSpace(text), calls, thoughtSignature),
		ThoughtSignature: thoughtSignature,
	}
	return s.SaveAssistantResponse(ctx, response, "")
}

func (s *MemorySessionStore) SaveAssistantResponse(ctx context.Context, response Response, _ string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	blocks := response.Blocks
	if len(blocks) == 0 {
		blocks = responseBlocksFromParts(strings.TrimSpace(response.Text), response.ToolCalls, response.ThoughtSignature)
	}
	msg := TypedMessage{
		Role:   string(RoleAssistant),
		Blocks: cloneBlocks(blocks),
	}
	for _, call := range response.ToolCalls {
		if call.ID != "" {
			s.toolName[call.ID] = call.Name
		}
	}
	if len(msg.Blocks) == 0 {
		return s.path, s.nextMessageIDLocked(), nil
	}

	s.messages = append(s.messages, msg)
	return s.path, s.nextMessageIDLocked(), nil
}

func (s *MemorySessionStore) SaveToolResults(ctx context.Context, results []ToolResult, additionalText string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	msg := TypedMessage{
		Role:   string(RoleUser),
		Blocks: make([]Block, 0, 1+len(results)),
	}
	if additionalText != "" {
		msg.Blocks = append(msg.Blocks, TextBlock{Text: additionalText})
	}
	for _, result := range results {
		msg.Blocks = append(msg.Blocks, ToolResultBlockFromResult(result, s.toolName[result.ID]))
	}
	s.messages = append(s.messages, msg)
	return s.path, s.nextMessageIDLocked(), nil
}

func (s *MemorySessionStore) UpdatePath(newPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if newPath != "" {
		s.path = newPath
	}
}

func (s *MemorySessionStore) GetPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *MemorySessionStore) nextMessageIDLocked() string {
	s.nextID++
	return fmt.Sprintf("memory_%04d", s.nextID)
}

func cloneTypedMessages(messages []TypedMessage) []TypedMessage {
	cloned := make([]TypedMessage, len(messages))
	for i, msg := range messages {
		cloned[i].Role = msg.Role
		cloned[i].Blocks = cloneBlocks(msg.Blocks)
	}
	return cloned
}

func cloneBlocks(blocks []Block) []Block {
	cloned := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case TextBlock:
			cloned = append(cloned, b)
		case ToolUseBlock:
			b.Input = append([]byte(nil), b.Input...)
			cloned = append(cloned, b)
		case ToolResultBlock:
			cloned = append(cloned, b)
		case ReasoningBlock:
			b.Summary = append([]byte(nil), b.Summary...)
			b.Content = append([]byte(nil), b.Content...)
			b.Raw = append([]byte(nil), b.Raw...)
			cloned = append(cloned, b)
		case ImageBlock:
			cloned = append(cloned, b)
		case AudioBlock:
			cloned = append(cloned, b)
		case FileBlock:
			cloned = append(cloned, b)
		}
	}
	return cloned
}
