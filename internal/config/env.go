package config

import "strings"

// Environments lists the supported environments
var Environments = []string{"prod", "dev"}

// IsValidEnvironment checks if the environment is valid or a custom URL
func IsValidEnvironment(env string) bool {
	// Check if it's a known environment
	for _, e := range Environments {
		if e == env {
			return true
		}
	}

	// Check if it's a custom URL
	lowerEnv := strings.ToLower(env)
	if strings.HasPrefix(lowerEnv, "http://") || strings.HasPrefix(lowerEnv, "https://") {
		return true
	}

	return false
}
