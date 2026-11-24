package session

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"slices"
)

// storedBlock is one typed block in a content file. Type names the block,
// and the other fields hold whichever of its parts the block has.
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
	// Images are the images a tool result carries, such as the file a
	// view_image call read.
	Images []storedImage `json:"images,omitempty"`
}

type storedImage struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
	Name   string `json:"name,omitempty"`
}

func clone(raw json.RawMessage) json.RawMessage {
	return slices.Clone(raw)
}

func storeBlock(block core.Block) (storedBlock, error) {
	switch b := block.(type) {
	case core.TextBlock:
		return storedBlock{Type: "text", Text: b.Text}, nil
	case core.ReasoningBlock:
		return storedBlock{
			Type: "reasoning", Provider: b.Provider, ReasoningType: b.Type, ID: b.ID, Status: b.Status,
			Text: b.Text, Summary: clone(b.Summary), Content: clone(b.Content), Signature: b.Signature,
			EncryptedContent: b.EncryptedContent, Raw: clone(b.Raw),
		}, nil
	case core.ToolUseBlock:
		return storedBlock{
			Type: "tool_use", ID: b.ID, Name: b.Name, ToolType: b.Type, Namespace: b.Namespace,
			OriginalName: b.OriginalName, Input: clone(b.Input), InputString: b.InputString,
		}, nil
	case core.ToolResultBlock:
		stored := storedBlock{
			Type: "tool_result", ToolUseID: b.ToolUseID, ToolType: b.Type, Namespace: b.Namespace,
			Name: b.Name, Text: b.Content, IsError: b.IsError,
		}
		for _, image := range b.Images {
			stored.Images = append(stored.Images, storedImage{URL: image.URL, Detail: image.Detail, Name: image.Name})
		}
		return stored, nil
	case core.ImageBlock:
		return storedBlock{Type: "image", URL: b.URL, Detail: b.Detail, Name: b.Name}, nil
	case core.AudioBlock:
		return storedBlock{Type: "audio", AudioID: b.ID, Data: b.Data, Format: b.Format, URL: b.URL, Duration: b.Duration}, nil
	case core.FileBlock:
		return storedBlock{Type: "file", FileID: b.FileID}, nil
	}
	return storedBlock{}, fmt.Errorf("no stored form for block %T", block)
}

func loadBlock(b storedBlock) (core.Block, error) {
	switch b.Type {
	case "text":
		return core.TextBlock{Text: b.Text}, nil
	case "reasoning":
		return core.ReasoningBlock{
			Provider: b.Provider, Type: b.ReasoningType, ID: b.ID, Status: b.Status, Text: b.Text,
			Summary: clone(b.Summary), Content: clone(b.Content), Signature: b.Signature,
			EncryptedContent: b.EncryptedContent, Raw: clone(b.Raw),
		}, nil
	case "tool_use":
		return core.ToolUseBlock{
			ID: b.ID, Type: b.ToolType, Namespace: b.Namespace, OriginalName: b.OriginalName,
			Name: b.Name, Input: clone(b.Input), InputString: b.InputString,
		}, nil
	case "tool_result":
		result := core.ToolResultBlock{
			ToolUseID: b.ToolUseID, Type: b.ToolType, Namespace: b.Namespace, Name: b.Name,
			Content: b.Text, IsError: b.IsError,
		}
		for _, image := range b.Images {
			result.Images = append(result.Images, core.ImageBlock{URL: image.URL, Detail: image.Detail, Name: image.Name})
		}
		return result, nil
	case "image":
		return core.ImageBlock{URL: b.URL, Detail: b.Detail, Name: b.Name}, nil
	case "audio":
		return core.AudioBlock{ID: b.AudioID, Data: b.Data, Format: b.Format, URL: b.URL, Duration: b.Duration}, nil
	case "file":
		return core.FileBlock{FileID: b.FileID}, nil
	}
	return nil, fmt.Errorf("unknown stored block type %q", b.Type)
}
