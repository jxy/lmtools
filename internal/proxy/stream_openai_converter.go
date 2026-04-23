package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// OpenAIStreamConverter converts from various provider formats to OpenAI streaming format.
type OpenAIStreamConverter struct {
	writer           *OpenAIStreamWriter
	currentToolIndex int
	toolCallsByIndex map[int]*ToolCallDelta
	accumulatedText  string
	hasStarted       bool
	ctx              context.Context
	lastFinishReason string
	lastUsage        *OpenAIUsage
}

// NewOpenAIStreamConverter creates a new converter.
func NewOpenAIStreamConverter(writer *OpenAIStreamWriter, ctx context.Context) *OpenAIStreamConverter {
	return &OpenAIStreamConverter{
		writer:           writer,
		toolCallsByIndex: make(map[int]*ToolCallDelta),
		ctx:              ctx,
	}
}

func (c *OpenAIStreamConverter) ensureTextStart() error {
	if c.hasStarted {
		return nil
	}
	if err := c.writer.WriteInitialAssistantTextDelta(); err != nil {
		return err
	}
	c.hasStarted = true
	return nil
}

func (c *OpenAIStreamConverter) writeToolCallStart(index int, toolCall *ToolCallDelta) error {
	if !c.hasStarted {
		if err := c.writer.WriteInitialAssistantToolCallDelta(index, toolCall.ID, toolCall.Function.Name); err != nil {
			return err
		}
		c.hasStarted = true
		return nil
	}
	return c.writer.WriteToolCallDelta(index, toolCall, nil, nil)
}

// HandleAnthropicEvent processes an Anthropic streaming event and converts to OpenAI format.
func (c *OpenAIStreamConverter) HandleAnthropicEvent(eventType string, data json.RawMessage) error {
	switch eventType {
	case EventMessageStart:
		return nil

	case EventContentBlockStart:
		var event ContentBlockStartEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		if event.ContentBlock.Type == "tool_use" {
			toolCall := &ToolCallDelta{
				Index: event.Index,
				ID:    event.ContentBlock.ID,
				Type:  "function",
				Function: &FunctionCallDelta{
					Name:      event.ContentBlock.Name,
					Arguments: "",
				},
			}
			c.toolCallsByIndex[event.Index] = toolCall

			if err := c.writeToolCallStart(event.Index, toolCall); err != nil {
				return err
			}
		}

	case EventContentBlockDelta:
		var event ContentBlockDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		switch event.Delta.Type {
		case "text_delta":
			if err := c.ensureTextStart(); err != nil {
				return err
			}
			if err := c.writer.WriteContent(event.Delta.Text); err != nil {
				return err
			}
			c.accumulatedText += event.Delta.Text

		case "input_json_delta":
			if toolCall, ok := c.toolCallsByIndex[event.Index]; ok {
				arg := ""
				if event.Delta.PartialJSON != nil {
					arg = *event.Delta.PartialJSON
				}
				deltaCall := &ToolCallDelta{
					Index: event.Index,
					Function: &FunctionCallDelta{
						Arguments: arg,
					},
				}
				if err := c.writer.WriteToolCallDelta(event.Index, deltaCall, nil, nil); err != nil {
					return err
				}
				toolCall.Function.Arguments += arg
			}
		}

	case EventMessageDelta:
		var event MessageDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		if event.Delta.StopReason != "" {
			c.lastFinishReason = MapStopReasonToOpenAIFinishReason(event.Delta.StopReason)
		}
		if event.Usage != nil {
			c.lastUsage = AnthropicUsageToOpenAI(event.Usage)
		}

	case EventMessageStop:
		finish := c.lastFinishReason
		if finish == "" {
			finish = "stop"
		}
		if !c.hasStarted {
			if err := c.ensureTextStart(); err != nil {
				return err
			}
		}
		return c.writer.WriteFinish(finish, c.lastUsage)

	case EventError:
		var event ErrorEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return c.writer.WriteError(event.Error.Type, event.Error.Message)

	case EventPing:
		return nil
	}

	return nil
}

// HandleGoogleChunk processes a Google streaming chunk and converts to OpenAI format.
func (c *OpenAIStreamConverter) HandleGoogleChunk(chunk map[string]interface{}) error {
	if usage, ok := chunk["usageMetadata"].(map[string]interface{}); ok {
		c.lastUsage = &OpenAIUsage{
			PromptTokens:     int(usage["promptTokenCount"].(float64)),
			CompletionTokens: int(usage["candidatesTokenCount"].(float64)),
			TotalTokens:      int(usage["totalTokenCount"].(float64)),
		}
	}

	candidates, ok := chunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}

	candidate := candidates[0].(map[string]interface{})
	finishReason, _ := candidate["finishReason"].(string)

	if content, ok := candidate["content"].(map[string]interface{}); ok {
		if parts, ok := content["parts"].([]interface{}); ok {
			for _, part := range parts {
				partMap := part.(map[string]interface{})

				if text, ok := partMap["text"].(string); ok && text != "" {
					if err := c.ensureTextStart(); err != nil {
						return err
					}
					if err := c.writer.WriteContent(text); err != nil {
						return err
					}
				}

				if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
					name := functionCall["name"].(string)
					args, _ := json.Marshal(functionCall["args"])
					toolID, _ := functionCall["id"].(string)
					if toolID == "" {
						toolID = fmt.Sprintf("call_%x", time.Now().UnixNano())
					}

					toolCall := &ToolCallDelta{
						Index: c.currentToolIndex,
						ID:    toolID,
						Type:  "function",
						Function: &FunctionCallDelta{
							Name:      name,
							Arguments: string(args),
						},
					}

					if err := c.writeToolCallStart(c.currentToolIndex, toolCall); err != nil {
						return err
					}
					c.currentToolIndex++
				}
			}
		}
	}

	if finishReason != "" {
		mapped := mapGoogleFinishReason(finishReason)
		if !c.hasStarted {
			if err := c.ensureTextStart(); err != nil {
				return err
			}
		}
		return c.writer.WriteFinish(mapped, c.lastUsage)
	}

	return nil
}

// HandleArgoText processes plain text from Argo and converts to OpenAI format.
func (c *OpenAIStreamConverter) HandleArgoText(text string) error {
	if err := c.ensureTextStart(); err != nil {
		return err
	}
	return c.writer.WriteContent(text)
}

// Complete sends the completion sequence for OpenAI format.
func (c *OpenAIStreamConverter) Complete(finishReason string) error {
	return c.FinishStream(finishReason, nil)
}

// FinishStream sends the completion sequence with optional usage information.
func (c *OpenAIStreamConverter) FinishStream(finishReason string, usage *OpenAIUsage) error {
	if !c.hasStarted {
		if err := c.ensureTextStart(); err != nil {
			return err
		}
	}
	return c.writer.WriteFinish(finishReason, usage)
}
