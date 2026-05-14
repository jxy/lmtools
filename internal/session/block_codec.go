package session

import (
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"os"
)

const messageBlocksVersion = 1

type storedMessageBlocks struct {
	Version int           `json:"version"`
	Role    core.Role     `json:"role"`
	Blocks  []storedBlock `json:"blocks"`
}

type storedBlock struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	ReasoningType    string          `json:"reasoning_type,omitempty"`
	ID               string          `json:"id,omitempty"`
	Status           string          `json:"status,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Raw              json.RawMessage `json:"raw,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolType         string          `json:"tool_type,omitempty"`
	Namespace        string          `json:"namespace,omitempty"`
	OriginalName     string          `json:"original_name,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	InputString      string          `json:"input_string,omitempty"`
	ToolUseID        string          `json:"tool_use_id,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	URL              string          `json:"url,omitempty"`
	Detail           string          `json:"detail,omitempty"`
	FileID           string          `json:"file_id,omitempty"`
	AudioID          string          `json:"audio_id,omitempty"`
	Data             string          `json:"data,omitempty"`
	Format           string          `json:"format,omitempty"`
	Duration         int             `json:"duration,omitempty"`
}

func marshalMessageBlocks(msg Message, toolInteraction *core.ToolInteraction) ([]byte, error) {
	blocks := blocksFromMessageProjection(msg, toolInteraction)
	return marshalExplicitMessageBlocks(msg.Role, blocks)
}

func marshalExplicitMessageBlocks(role core.Role, blocks []core.Block) ([]byte, error) {
	if len(blocks) == 0 {
		return nil, nil
	}
	stored, err := storedBlocksFromCore(blocks)
	if err != nil {
		return nil, err
	}
	payload := storedMessageBlocks{
		Version: messageBlocksVersion,
		Role:    role,
		Blocks:  stored,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, errors.WrapError("marshal message blocks", err)
	}
	return data, nil
}

func blocksFromMessageProjection(msg Message, toolInteraction *core.ToolInteraction) []core.Block {
	blocks := make([]core.Block, 0)
	if msg.ThoughtSignature != "" {
		blocks = append(blocks, core.ReasoningBlock{
			Provider:  "google",
			Type:      "thought_signature",
			Signature: msg.ThoughtSignature,
		})
	}
	if msg.Content != "" {
		blocks = append(blocks, core.TextBlock{Text: msg.Content})
	}
	if toolInteraction == nil {
		return blocks
	}
	for _, call := range toolInteraction.Calls {
		if call.ThoughtSignature != "" {
			blocks = append(blocks, core.ReasoningBlock{
				Provider:  "google",
				Type:      "thought_signature",
				Signature: call.ThoughtSignature,
			})
		}
		blocks = append(blocks, core.ToolUseBlock{
			ID:           call.ID,
			Type:         call.Type,
			Namespace:    call.Namespace,
			OriginalName: call.OriginalName,
			Name:         call.Name,
			Input:        append(json.RawMessage(nil), call.Args...),
			InputString:  call.Input,
		})
	}
	for _, result := range toolInteraction.Results {
		blocks = append(blocks, core.ToolResultBlockFromResult(result, ""))
	}
	return blocks
}

func storedBlocksFromCore(blocks []core.Block) ([]storedBlock, error) {
	stored := make([]storedBlock, 0, len(blocks))
	for _, block := range blocks {
		switch value := block.(type) {
		case core.TextBlock:
			stored = append(stored, storedBlock{Type: "text", Text: value.Text})
		case core.ReasoningBlock:
			stored = append(stored, storedBlock{
				Type:             "reasoning",
				Provider:         value.Provider,
				ReasoningType:    value.Type,
				ID:               value.ID,
				Status:           value.Status,
				Text:             value.Text,
				Summary:          append(json.RawMessage(nil), value.Summary...),
				Content:          append(json.RawMessage(nil), value.Content...),
				Signature:        value.Signature,
				EncryptedContent: value.EncryptedContent,
				Raw:              append(json.RawMessage(nil), value.Raw...),
			})
		case core.ToolUseBlock:
			stored = append(stored, storedBlock{
				Type:         "tool_use",
				ID:           value.ID,
				Name:         value.Name,
				ToolType:     value.Type,
				Namespace:    value.Namespace,
				OriginalName: value.OriginalName,
				Input:        append(json.RawMessage(nil), value.Input...),
				InputString:  value.InputString,
			})
		case core.ToolResultBlock:
			stored = append(stored, storedBlock{
				Type:      "tool_result",
				ToolUseID: value.ToolUseID,
				ToolType:  value.Type,
				Namespace: value.Namespace,
				Name:      value.Name,
				Text:      value.Content,
				IsError:   value.IsError,
			})
		case core.ImageBlock:
			stored = append(stored, storedBlock{Type: "image", URL: value.URL, Detail: value.Detail})
		case core.AudioBlock:
			stored = append(stored, storedBlock{
				Type:     "audio",
				AudioID:  value.ID,
				Data:     value.Data,
				Format:   value.Format,
				URL:      value.URL,
				Duration: value.Duration,
			})
		case core.FileBlock:
			stored = append(stored, storedBlock{Type: "file", FileID: value.FileID})
		}
	}
	return stored, nil
}

func loadMessageBlocks(sessionPath, msgID string) ([]core.Block, bool, error) {
	path := buildMessageFilePaths(sessionPath, msgID).BlocksPath
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.WrapError("read message blocks", err)
	}
	var payload storedMessageBlocks
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, true, errors.WrapError("unmarshal message blocks", err)
	}
	blocks := make([]core.Block, 0, len(payload.Blocks))
	for _, block := range payload.Blocks {
		switch block.Type {
		case "text":
			blocks = append(blocks, core.TextBlock{Text: block.Text})
		case "reasoning":
			blocks = append(blocks, core.ReasoningBlock{
				Provider:         block.Provider,
				Type:             block.ReasoningType,
				ID:               block.ID,
				Status:           block.Status,
				Text:             block.Text,
				Summary:          append(json.RawMessage(nil), block.Summary...),
				Content:          append(json.RawMessage(nil), block.Content...),
				Signature:        block.Signature,
				EncryptedContent: block.EncryptedContent,
				Raw:              append(json.RawMessage(nil), block.Raw...),
			})
		case "tool_use":
			blocks = append(blocks, core.ToolUseBlock{
				ID:           block.ID,
				Type:         block.ToolType,
				Namespace:    block.Namespace,
				OriginalName: block.OriginalName,
				Name:         block.Name,
				Input:        append(json.RawMessage(nil), block.Input...),
				InputString:  block.InputString,
			})
		case "tool_result":
			blocks = append(blocks, core.ToolResultBlock{
				ToolUseID: block.ToolUseID,
				Type:      block.ToolType,
				Namespace: block.Namespace,
				Name:      block.Name,
				Content:   block.Text,
				IsError:   block.IsError,
			})
		case "image":
			blocks = append(blocks, core.ImageBlock{URL: block.URL, Detail: block.Detail})
		case "audio":
			blocks = append(blocks, core.AudioBlock{
				ID:       block.AudioID,
				Data:     block.Data,
				Format:   block.Format,
				URL:      block.URL,
				Duration: block.Duration,
			})
		case "file":
			blocks = append(blocks, core.FileBlock{FileID: block.FileID})
		}
	}
	return blocks, true, nil
}
