package models

import "strings"

// GetBaseURL returns the base URL for the given environment
func GetBaseURL(env string) string {
	switch strings.ToLower(env) {
	case "dev":
		return ArgoDevURL
	case "prod":
		return ArgoProdURL
	default:
		// If it looks like a URL, use it directly
		lowerEnv := strings.ToLower(env)
		if strings.HasPrefix(lowerEnv, "http://") || strings.HasPrefix(lowerEnv, "https://") {
			return env
		}
		// Default to prod
		return ArgoProdURL
	}
}
