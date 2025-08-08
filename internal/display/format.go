package display

import "fmt"

// FormatBytes formats byte count for display
func FormatBytes(bytes int) string {
	switch {
	case bytes < 1000:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 10*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	case bytes < 1024*1024:
		return fmt.Sprintf("%dKB", bytes/1024)
	case bytes < 10*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	default:
		return fmt.Sprintf("%dMB", bytes/(1024*1024))
	}
}

// FormatRole formats role with optional model
func FormatRole(role, model string) string {
	if role == "assistant" && model != "" {
		return fmt.Sprintf("%s/%s", role, model)
	}
	return role
}
