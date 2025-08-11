package session

import "lmtools/internal/core"

// Implement core.Session interface
func (s *Session) GetPath() string {
	return s.Path
}

// GetLineageAdapter wraps GetLineage to return core.Message types
func GetLineageAdapter(path string) ([]core.Message, error) {
	msgs, err := GetLineage(path)
	if err != nil {
		return nil, err
	}

	result := make([]core.Message, len(msgs))
	for i, msg := range msgs {
		result[i] = core.Message{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}
	return result, nil
}
