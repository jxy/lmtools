package argo

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const timestampLayout = "20060102T150405"

func CreateTimestampedFile(dir, op, ext string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	ts := time.Now().Format(timestampLayout)
	filename := fmt.Sprintf("%s_%s.%s", ts, op, ext)
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create file %s: %w", path, err)
	}
	return f, filename, nil
}

func LogJSON(dir, op string, payload []byte) error {
	f, _, err := CreateTimestampedFile(dir, op, "json")
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("failed to write payload: %v; additionally failed to close file: %w", err, cerr)
		}
		return fmt.Errorf("failed to write payload: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	return nil
}
