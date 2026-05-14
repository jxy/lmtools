package session

import (
	"lmtools/internal/errors"
	"os"
	"path/filepath"
)

type messageFilePaths struct {
	TxtPath    string
	JSONPath   string
	ToolsPath  string
	BlocksPath string
}

func buildMessageFilePaths(sessionPath, msgID string) messageFilePaths {
	basePath := filepath.Join(sessionPath, msgID)
	return messageFilePaths{
		TxtPath:    basePath + ".txt",
		JSONPath:   basePath + ".json",
		ToolsPath:  basePath + ".tools.json",
		BlocksPath: basePath + ".blocks.json",
	}
}

func writeStagedTempFile(dir, pattern string, data []byte) (string, error) {
	tmpFile, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()

	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", err
	}
	tmpFile = nil

	return tmpPath, nil
}

// writeFileAtomic writes data to a file atomically using rename.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	// Create temp file in same directory.
	tmpFile, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return errors.WrapError("create temp file", err)
	}
	tmpPath := tmpFile.Name()

	// Clean up on error.
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// Write data.
	if _, err := tmpFile.Write(data); err != nil {
		return errors.WrapError("write temp file", err)
	}

	// Sync to disk.
	if err := tmpFile.Sync(); err != nil {
		return errors.WrapError("sync temp file", err)
	}

	// Close before rename.
	if err := tmpFile.Close(); err != nil {
		return errors.WrapError("close temp file", err)
	}
	tmpFile = nil

	// Atomic rename.
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.WrapError("rename temp file", err)
	}

	return nil
}

// fileExists is a simple helper to check if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
