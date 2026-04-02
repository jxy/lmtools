package main

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
)

func ensureDirectoryPath(path, label string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", errors.WrapError("validate "+label, err)
	}

	if err := os.MkdirAll(absPath, constants.DirPerm); err != nil {
		return "", errors.WrapError("create "+label, err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", errors.WrapError("access "+label, err)
	}
	if !info.IsDir() {
		return "", errors.WrapError("validate "+label, fmt.Errorf("%s path exists but is not a directory: %s", label, absPath))
	}

	return absPath, nil
}
