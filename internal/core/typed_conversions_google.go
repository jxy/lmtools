package core

// ToGoogleTyped converts TypedMessage to strongly typed Google format.
func ToGoogleTyped(messages []TypedMessage) []GoogleMessage {
	return toGoogleTypedInternal(messages, false)
}

// ToGoogleForArgoTyped converts TypedMessage to Google format for Argo (keeps system messages).
func ToGoogleForArgoTyped(messages []TypedMessage) []GoogleMessage {
	return toGoogleTypedInternal(messages, true)
}

// toGoogleTypedInternal converts TypedMessage to strongly typed Google format.
// This is a pure function with no side effects.
func toGoogleTypedInternal(messages []TypedMessage, keepSystem bool) []GoogleMessage {
	result := make([]GoogleMessage, 0, len(messages))

	for _, msg := range messages {
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		if msg.Role == string(RoleSystem) && !keepSystem {
			continue
		}

		googleMsg := GoogleMessage{Role: role}
		parts := make([]GooglePart, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case TextBlock:
				parts = append(parts, GooglePart{Text: b.Text})
			case ImageBlock:
				parts = append(parts, GooglePart{Text: "[Image: " + b.URL + "]"})
			case AudioBlock:
				audioText := "[Audio content"
				if b.ID != "" {
					audioText += ": " + b.ID
				} else if b.Data != "" {
					audioText += " (data)"
				}
				audioText += "]"
				parts = append(parts, GooglePart{Text: audioText})
			case FileBlock:
				parts = append(parts, GooglePart{Text: "[File content: " + b.FileID + "]"})
			case ToolUseBlock:
				parts = append(parts, GooglePart{
					FunctionCall: &GoogleFunctionCall{
						Name: b.Name,
						Args: b.Input,
					},
				})
			case ToolResultBlock:
				functionName := b.Name
				if functionName == "" {
					// TODO: Implement proper mapping from tool_use_id to function name
					functionName = b.ToolUseID
				}
				parts = append(parts, GooglePart{
					FunctionResponse: &GoogleFunctionResponse{
						Name: functionName,
						Response: GoogleResponseContent{
							Content: b.Content,
							Error:   b.IsError,
						},
					},
				})
			}
		}

		if len(parts) > 0 {
			googleMsg.Parts = parts
			result = append(result, googleMsg)
		}
	}

	return result
}

// MarshalGoogleMessagesForRequest converts typed Google messages to []interface{} for request bodies.
func MarshalGoogleMessagesForRequest(messages []GoogleMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		msgMap := map[string]interface{}{
			"role": msg.Role,
		}

		if len(msg.Parts) > 0 {
			partMaps := make([]map[string]interface{}, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				partMaps = append(partMaps, part.ToMap())
			}
			msgMap["parts"] = partMaps
		}

		result = append(result, msgMap)
	}
	return result
}
