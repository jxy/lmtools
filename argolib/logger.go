package argo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const timestampLayout = "20060102T150405"

func CreateTimestampedFile(dir, op, ext string) (*os.File, string, error) {
	// Validate inputs to prevent directory traversal attacks
	if strings.Contains(op, "..") || strings.Contains(ext, "..") {
		return nil, "", fmt.Errorf("invalid characters in operation or extension")
	}

	// Clean the directory path
	dir = filepath.Clean(dir)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Generate unique suffix to prevent race conditions
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, "", fmt.Errorf("failed to generate random suffix: %w", err)
	}

	ts := time.Now().Format(timestampLayout)
	pid := os.Getpid()
	randSuffix := hex.EncodeToString(randBytes)

	filename := fmt.Sprintf("%s_%s_%d_%s.%s", ts, op, pid, randSuffix, ext)
	path := filepath.Join(dir, filename)

	// Use O_EXCL to prevent overwrite in case of collision
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
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

	// Write and sync to ensure data is persisted
	if _, err := f.Write(payload); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("failed to write payload: %v; additionally failed to close file: %w", err, cerr)
		}
		return fmt.Errorf("failed to write payload: %w", err)
	}

	// Sync to ensure data is flushed to disk
	if err := f.Sync(); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("failed to sync file: %v; additionally failed to close file: %w", err, cerr)
		}
		return fmt.Errorf("failed to sync file: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	return nil
}
