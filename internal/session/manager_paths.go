package session

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveSessionPath returns an absolute cleaned path within the sessions tree when given a relative path.
func (m *Manager) ResolveSessionPath(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.SessionsDir(), path)
	}
	return filepath.Clean(path)
}

// SessionID returns the path relative to the sessions root when possible.
func (m *Manager) SessionID(sessionPath string) string {
	sessionPath = m.ResolveSessionPath(sessionPath)
	relPath, err := filepath.Rel(m.SessionsDir(), sessionPath)
	if err != nil {
		return filepath.Base(sessionPath)
	}
	return relPath
}

// IsWithinSessionsDir reports whether the given path is inside the sessions root.
func (m *Manager) IsWithinSessionsDir(path string) bool {
	sessionsDir := m.SessionsDir()
	path = filepath.Clean(path)
	return path == sessionsDir || strings.HasPrefix(path, sessionsDir+string(filepath.Separator))
}

// ParseSessionPath breaks down a session path into root and components.
func (m *Manager) ParseSessionPath(path string) (root string, components []string) {
	sessionsDir := m.SessionsDir()

	relPath := path
	if strings.HasPrefix(path, sessionsDir) {
		relPath, _ = filepath.Rel(sessionsDir, path)
	}

	parts := strings.Split(relPath, string(filepath.Separator))
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." {
			filtered = append(filtered, part)
		}
	}

	if len(filtered) == 0 {
		return sessionsDir, nil
	}

	root = filepath.Join(sessionsDir, filtered[0])
	if len(filtered) > 1 {
		components = filtered[1:]
	}

	return root, components
}

// ParseMessageID extracts session path and message ID from a full message path.
func (m *Manager) ParseMessageID(messageIDPath string) (sessionPath string, messageID string) {
	sessionsDir := m.SessionsDir()

	if filepath.IsAbs(messageIDPath) {
		if info, err := os.Stat(messageIDPath); err == nil && info.IsDir() {
			return messageIDPath, ""
		}

		parts := strings.Split(messageIDPath, string(filepath.Separator))
		for i := len(parts) - 1; i >= 0; i-- {
			if !IsValidMessageID(parts[i]) {
				continue
			}

			messageID = parts[i]
			sessionPath = filepath.Join(parts[:i]...)
			if filepath.IsAbs(messageIDPath) && !filepath.IsAbs(sessionPath) {
				sessionPath = filepath.Join(string(filepath.Separator), sessionPath)
			}

			if strings.HasPrefix(sessionPath, sessionsDir+string(filepath.Separator)) || sessionPath == sessionsDir {
				if info, err := os.Stat(messageIDPath); err == nil && info.IsDir() {
					return messageIDPath, ""
				}
			}

			return sessionPath, messageID
		}
		return messageIDPath, ""
	}

	parts := strings.Split(messageIDPath, "/")
	if len(parts) < 2 {
		return filepath.Join(sessionsDir, messageIDPath), ""
	}

	lastPart := parts[len(parts)-1]
	if IsValidMessageID(lastPart) {
		return filepath.Join(sessionsDir, strings.Join(parts[:len(parts)-1], "/")), lastPart
	}

	return filepath.Join(sessionsDir, messageIDPath), ""
}

// IsSessionRoot checks if a path is a session root directory.
func (m *Manager) IsSessionRoot(sessionPath string) bool {
	return filepath.Dir(sessionPath) == m.SessionsDir()
}

// GetRootSession returns the root session directory from any nested path.
func (m *Manager) GetRootSession(sessionPath string) string {
	rootDir, _ := m.ParseSessionPath(sessionPath)
	return rootDir
}
