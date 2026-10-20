package session

import (
	"lmtools/internal/errors"
	"path/filepath"
)

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
