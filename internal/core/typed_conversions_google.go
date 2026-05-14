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
	toolNamesByID := make(map[string]string)

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
		firstFunctionCall := true
		pendingThoughtSignature := ""
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case ReasoningBlock:
				if b.Provider == "google" && b.Type == "thought_signature" {
					pendingThoughtSignature = b.Signature
				}
			case TextBlock:
				parts = append(parts, GooglePart{
					Text:             b.Text,
					ThoughtSignature: pendingThoughtSignature,
				})
				pendingThoughtSignature = ""
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
				if b.ID != "" && b.Name != "" {
					toolNamesByID[b.ID] = b.Name
				}
				input := b.Input
				if b.Type == "custom" {
					input = WrapCustomToolInput(CustomToolRawInput(b.InputString, b.Input))
				}
				thoughtSignature := pendingThoughtSignature
				if thoughtSignature == "" && firstFunctionCall {
					thoughtSignature = GoogleDummyThoughtSignature
				}
				parts = append(parts, GooglePart{
					FunctionCall: &GoogleFunctionCall{
						Name: b.Name,
						Args: input,
					},
					ThoughtSignature: thoughtSignature,
				})
				pendingThoughtSignature = ""
				firstFunctionCall = false
			case ToolResultBlock:
				functionName := b.Name
				if functionName == "" {
					functionName = toolNamesByID[b.ToolUseID]
				}
				if functionName == "" {
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
		if pendingThoughtSignature != "" {
			parts = append(parts, GooglePart{ThoughtSignature: pendingThoughtSignature})
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
