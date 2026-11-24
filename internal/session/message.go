package session

import (
	"encoding/json"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"slices"
	"time"
)

// Message is the metadata of a stored message. Its content is stored apart,
// so walking a tree reads only metadata.
type Message struct {
	ID string `json:"id"`
	// Parent is the message this one follows, or empty when it follows the
	// session itself.
	Parent     string    `json:"parent"`
	Role       core.Role `json:"role"`
	Time       time.Time `json:"time"`
	Model      string    `json:"model,omitempty"`
	Incomplete bool      `json:"incomplete,omitempty"`
	// Settings, on a system record, are the conversation's settings from
	// this message on.
	Settings *Settings `json:"settings,omitempty"`
	// Pin, on a pin event, is the standing pin it adds, replaces or removes.
	Pin *contextstate.PinChange `json:"pin,omitempty"`
	// Receipt, on a reply, records the window it was produced with.
	Receipt *contextstate.Receipt `json:"receipt,omitempty"`
}

// Content is what a message says: its typed blocks, and for a message that
// calls tools or carries their results, the tool record, which holds what
// operators and the invocation journal need beyond the blocks.
type Content struct {
	Blocks []core.Block
	Tools  *core.ToolInteraction
}

// Entry is a message to append.
type Entry struct {
	Role core.Role
	// Time is when the message was made; the zero time stands for the
	// moment it is appended.
	Time       time.Time
	Model      string
	Incomplete bool
	Settings   *Settings
	Pin        *contextstate.PinChange
	Receipt    *contextstate.Receipt
	Content    Content
}

// Typed returns the message in the form requests carry.
func Typed(m Message, c Content) core.TypedMessage {
	return core.TypedMessage{Role: string(m.Role), Blocks: slices.Clone(c.Blocks)}
}

// History is the lineage of a message with the content of each message,
// oldest first.
type History struct {
	Session  string
	Messages []Message
	Contents []Content
}

// Ref names message i of the history.
func (h History) Ref(i int) Ref {
	return Ref{Session: h.Session, Message: h.Messages[i].ID}
}

// Typed returns the history in the form requests carry. A tool result that
// was saved without its tool's name takes the name of the call it answers.
func (h History) Typed() []core.TypedMessage {
	names := map[string]string{}
	typed := make([]core.TypedMessage, len(h.Messages))
	for i := range h.Messages {
		typed[i] = Typed(h.Messages[i], h.Contents[i])
		for j, block := range typed[i].Blocks {
			switch b := block.(type) {
			case core.ToolUseBlock:
				if b.ID != "" {
					names[b.ID] = b.Name
				}
			case core.ToolResultBlock:
				if b.Name == "" && b.ToolUseID != "" {
					b.Name = names[b.ToolUseID]
					typed[i].Blocks[j] = b
				}
			}
		}
	}
	return typed
}

// storedContent is a message's content file.
type storedContent struct {
	Blocks []storedBlock         `json:"blocks"`
	Tools  *core.ToolInteraction `json:"tools,omitempty"`
}

func encodeContent(c Content) ([]byte, error) {
	stored := storedContent{Blocks: make([]storedBlock, 0, len(c.Blocks))}
	for _, block := range c.Blocks {
		b, err := storeBlock(block)
		if err != nil {
			return nil, err
		}
		stored.Blocks = append(stored.Blocks, b)
	}
	if c.Tools != nil && (len(c.Tools.Calls) > 0 || len(c.Tools.Results) > 0) {
		stored.Tools = c.Tools
	}
	// Compact, so the raw JSON a block carries, such as a tool's input or a
	// provider's reasoning item, keeps its bytes.
	data, err := json.Marshal(stored)
	if err != nil {
		return nil, lmerrors.WrapError("encode message content", err)
	}
	return append(data, '\n'), nil
}

func decodeContent(data []byte) (Content, error) {
	var stored storedContent
	if err := json.Unmarshal(data, &stored); err != nil {
		return Content{}, lmerrors.WrapError("decode message content", err)
	}
	c := Content{Tools: stored.Tools, Blocks: make([]core.Block, 0, len(stored.Blocks))}
	for _, block := range stored.Blocks {
		b, err := loadBlock(block)
		if err != nil {
			return Content{}, err
		}
		c.Blocks = append(c.Blocks, b)
	}
	return c, nil
}
