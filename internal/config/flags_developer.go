package config

import (
	"fmt"
	"lmtools/internal/core"
)

func validateDeveloperFlags(cfg Config) error {
	if cfg.Developer == "" {
		return nil
	}
	if cfg.ShowSessions || cfg.Show != "" || cfg.Delete != "" || cfg.ListModels {
		return fmt.Errorf("-developer is only supported when sending a chat request")
	}
	return core.ValidateDeveloperInstructions(cfg.RequestOptions())
}
