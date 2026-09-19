package config

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
)

// imagePathsFlag collects every -image argument in command-line order, which
// is the order the images are placed in the user turn.
type imagePathsFlag struct {
	paths *[]string
}

func (f imagePathsFlag) String() string {
	if f.paths == nil {
		return ""
	}
	return strings.Join(*f.paths, ",")
}

func (f imagePathsFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("image path cannot be empty")
	}
	*f.paths = append(*f.paths, value)
	return nil
}

// validateImageFlags loads every -image file at parse time, the way
// -json-schema reads its file, so a missing or unreadable image fails before
// any credential is used or any session is touched. The loaded blocks carry
// the -image-detail hint; renderers that have no such field ignore it.
func validateImageFlags(cfg *Config) error {
	detail := strings.ToLower(strings.TrimSpace(cfg.ImageDetail))
	if len(cfg.ImagePaths) == 0 {
		if detail != "" {
			return fmt.Errorf("-image-detail requires -image")
		}
		return nil
	}

	if cfg.Embed {
		return fmt.Errorf("invalid flag combination: -image is only supported in chat mode")
	}
	if cfg.ArgoLegacy {
		return fmt.Errorf("invalid flag combination: -image is not supported with -argo-legacy")
	}
	if detail != "" && !isValidImageDetailFlag(detail) {
		return fmt.Errorf("-image-detail must be one of: auto, low, high")
	}

	cfg.Images = make([]core.ImageBlock, 0, len(cfg.ImagePaths))
	for _, path := range cfg.ImagePaths {
		image, err := core.LoadImageFile(path, constants.MaxCLIImageBytes)
		if err != nil {
			return fmt.Errorf("-image: %w", err)
		}
		image.Detail = detail
		cfg.Images = append(cfg.Images, image)
	}
	return nil
}

func isValidImageDetailFlag(value string) bool {
	switch value {
	case "auto", "low", "high":
		return true
	default:
		return false
	}
}
