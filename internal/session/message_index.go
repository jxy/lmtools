package session

import (
	"fmt"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
)

// indexMessageDirectories builds a map of message ID to directory path for efficient lookups.
// This function walks the session tree once and returns a map where keys are message IDs
// and values are the directory paths containing those messages.
func indexMessageDirectories(sessionPath string) (map[string]string, error) {
	index := make(map[string]string)

	var indexDir func(dirPath string) error
	indexDir = func(dirPath string) error {
		msgIDs, err := listMessages(dirPath)
		if err != nil {
			return errors.WrapError("list messages in "+dirPath, err)
		}
		for _, msgID := range msgIDs {
			index[msgID] = dirPath
		}

		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return errors.WrapError(fmt.Sprintf("read directory %s", dirPath), err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if isSibling, _, _ := IsSiblingDir(entry.Name()); isSibling {
				if err := indexDir(filepath.Join(dirPath, entry.Name())); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := indexDir(sessionPath); err != nil {
		return nil, err
	}
	return index, nil
}

func indexMessagesAlongPathWithManager(manager *Manager, sessionPath string) (map[string]string, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)

	rootDir, components := manager.ParseSessionPath(sessionPath)
	index := make(map[string]string)
	currentDir := rootDir

	dirs := []string{currentDir}
	for _, comp := range components {
		currentDir = filepath.Join(currentDir, comp)
		dirs = append(dirs, currentDir)
	}

	for _, dir := range dirs {
		msgs, err := listMessages(dir)
		if err != nil {
			return nil, errors.WrapError("list messages in "+dir, err)
		}
		for _, msgID := range msgs {
			index[msgID] = dir
		}
	}

	return index, nil
}
