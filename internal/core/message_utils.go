package core

// splitSystem extracts system message from typed messages if present
// It returns the system content and the remaining messages without the system message
func splitSystem(messages []TypedMessage) (system string, rest []TypedMessage) {
	if len(messages) > 0 && messages[0].Role == string(RoleSystem) {
		if len(messages[0].Blocks) > 0 {
			if tb, ok := messages[0].Blocks[0].(TextBlock); ok {
				system = tb.Text
			}
		}
		return system, messages[1:]
	}
	return "", messages
}
