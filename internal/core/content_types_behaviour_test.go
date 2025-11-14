package core

import (
	"testing"
)

func TestConvertToContent_UnknownTypeLogsDebug(t *testing.T) {
	// Unknown type should default to empty text
	input := map[string]interface{}{"type": "unknown_type"}
	c := ConvertToContent(input)
	if c.GetType() != ContentTypeText || c.GetText() != "" {
		t.Errorf("expected TextContent with empty text, got type=%s text=%q", c.GetType(), c.GetText())
	}
}

func TestConvertToContent_UnexpectedInputTypeLogsDebug(t *testing.T) {
	// Non-supported type (e.g., int) should default to empty text
	c := ConvertToContent(42)
	if c.GetType() != ContentTypeText || c.GetText() != "" {
		t.Errorf("expected TextContent with empty text, got type=%s text=%q", c.GetType(), c.GetText())
	}
}
