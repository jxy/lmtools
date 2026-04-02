package session

import "fmt"

// formatVariableWidthHexID keeps the historical 4-8 digit hex formatting used
// for session and message identifiers while centralizing the logic in one place.
func formatVariableWidthHexID(id int) string {
	switch {
	case id <= 0xffff:
		return fmt.Sprintf("%04x", id)
	case id <= 0xfffff:
		return fmt.Sprintf("%05x", id)
	case id <= 0xffffff:
		return fmt.Sprintf("%06x", id)
	case id <= 0xfffffff:
		return fmt.Sprintf("%07x", id)
	default:
		return fmt.Sprintf("%08x", id)
	}
}
