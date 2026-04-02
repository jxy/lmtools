package session

import (
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestManagerCreateAndLoadSession(t *testing.T) {
	sessionsDir := t.TempDir()
	manager := NewManager(sessionsDir)

	created, err := manager.CreateSession("system prompt", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if created.SessionsDir != sessionsDir {
		t.Fatalf("created.SessionsDir = %q, want %q", created.SessionsDir, sessionsDir)
	}

	loaded, err := manager.LoadSession(filepath.Base(created.Path))
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}

	if loaded.Path != created.Path {
		t.Fatalf("loaded.Path = %q, want %q", loaded.Path, created.Path)
	}
	if loaded.SessionsDir != sessionsDir {
		t.Fatalf("loaded.SessionsDir = %q, want %q", loaded.SessionsDir, sessionsDir)
	}
}

func TestManagerPathHelpers(t *testing.T) {
	sessionsDir := t.TempDir()
	manager := NewManager(sessionsDir)

	relativePath := filepath.Join("001a", "0002.s.0000")
	resolvedPath := filepath.Join(sessionsDir, relativePath)
	if got := manager.ResolveSessionPath(relativePath); got != resolvedPath {
		t.Fatalf("ResolveSessionPath(%q) = %q, want %q", relativePath, got, resolvedPath)
	}
	if got := manager.SessionID(resolvedPath); got != relativePath {
		t.Fatalf("SessionID(%q) = %q, want %q", resolvedPath, got, relativePath)
	}
	if !manager.IsWithinSessionsDir(resolvedPath) {
		t.Fatalf("IsWithinSessionsDir(%q) = false, want true", resolvedPath)
	}

	root, components := manager.ParseSessionPath(filepath.Join(sessionsDir, "001a", "0002.s.0000"))
	expectedRoot := filepath.Join(sessionsDir, "001a")
	expectedComponents := []string{"0002.s.0000"}
	if root != expectedRoot {
		t.Fatalf("ParseSessionPath() root = %q, want %q", root, expectedRoot)
	}
	if !reflect.DeepEqual(components, expectedComponents) {
		t.Fatalf("ParseSessionPath() components = %#v, want %#v", components, expectedComponents)
	}

	sessionPath, messageID := manager.ParseMessageID("001a/0002.s.0000/0001")
	expectedSessionPath := filepath.Join(sessionsDir, "001a", "0002.s.0000")
	if sessionPath != expectedSessionPath || messageID != "0001" {
		t.Fatalf("ParseMessageID() = (%q, %q), want (%q, %q)", sessionPath, messageID, expectedSessionPath, "0001")
	}

	rootSession := filepath.Join(sessionsDir, "00ff")
	if err := os.MkdirAll(rootSession, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if !manager.IsSessionRoot(rootSession) {
		t.Fatalf("IsSessionRoot(%q) = false, want true", rootSession)
	}

	branchPath := filepath.Join(rootSession, "0002.s.0000", "0001.s.0001")
	if got := manager.GetRootSession(branchPath); got != rootSession {
		t.Fatalf("GetRootSession(%q) = %q, want %q", branchPath, got, rootSession)
	}
}
