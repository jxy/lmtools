package session

import (
	stdErrors "errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// ManagerConfig configures the default session manager.
type ManagerConfig struct {
	SessionsDir    string
	SkipFlockCheck bool
}

// Manager owns session directory resolution and flock-check state.
type Manager struct {
	mu             sync.RWMutex
	sessionsDir    string
	skipFlockCheck bool
	flockChecked   bool
}

var defaultManager = &Manager{}

// DefaultManager returns the package-level manager used by compatibility wrappers.
func DefaultManager() *Manager {
	return defaultManager
}

// NewManager creates a manager with an optional explicit sessions directory.
func NewManager(sessionsDir string) *Manager {
	return &Manager{sessionsDir: sessionsDir}
}

// ConfigureDefaultManager applies session-related configuration through one seam.
func ConfigureDefaultManager(cfg ManagerConfig) {
	defaultManager.SetSessionsDir(cfg.SessionsDir)
	defaultManager.SetSkipFlockCheck(cfg.SkipFlockCheck)
}

// SetSessionsDir sets a custom sessions directory.
func (m *Manager) SetSessionsDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionsDir = dir
	m.flockChecked = false
}

// SetSkipFlockCheck sets whether to skip the file locking check.
func (m *Manager) SetSkipFlockCheck(skip bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skipFlockCheck = skip
	m.flockChecked = false
}

// SessionsDir returns the base directory for all sessions.
func (m *Manager) SessionsDir() string {
	m.mu.RLock()
	baseDir := m.sessionsDir
	m.mu.RUnlock()

	if baseDir != "" {
		return baseDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".lmc", "sessions")
	}
	return filepath.Join(homeDir, ".lmc", "sessions")
}

// TestFlockSupport checks if the filesystem supports flock.
func (m *Manager) TestFlockSupport() error {
	sessionsDir := m.SessionsDir()
	parentDir := filepath.Dir(sessionsDir)
	if err := os.MkdirAll(parentDir, constants.DirPerm); err != nil {
		return errors.WrapError("create parent directory", err)
	}

	testFile, err := os.CreateTemp(parentDir, ".flock-test-*")
	if err != nil {
		return errors.WrapError("create test file", err)
	}
	defer os.Remove(testFile.Name())
	defer testFile.Close()

	fd := int(testFile.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.WrapError("flock test", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
		return errors.WrapError("flock unlock", err)
	}

	return nil
}

func (m *Manager) ensureFlockSupport(log core.Logger) {
	m.mu.RLock()
	checked := m.flockChecked
	skip := m.skipFlockCheck
	m.mu.RUnlock()

	if checked {
		return
	}

	if !skip {
		if err := m.TestFlockSupport(); err != nil && log != nil {
			log.Debugf("File locking may not work properly: %v", err)
			log.Debugf("Concurrent access to sessions may cause issues")
		}
	}

	m.mu.Lock()
	m.flockChecked = true
	m.mu.Unlock()
}

// CreateSession creates a new session with a sequential ID.
func (m *Manager) CreateSession(systemPrompt string, log core.Logger) (*Session, error) {
	m.ensureFlockSupport(log)

	sessionsDir := m.SessionsDir()
	if err := os.MkdirAll(sessionsDir, constants.DirPerm); err != nil {
		return nil, errors.WrapError("create sessions directory", err)
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, errors.WrapError("read sessions directory", err)
	}

	maxID := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if id, err := strconv.ParseUint(entry.Name(), 16, 64); err == nil {
			if int(id) > maxID {
				maxID = int(id)
			}
		}
	}

	for i := maxID + 1; i < maxID+100; i++ {
		sessionID := formatVariableWidthHexID(i)
		sessionPath := filepath.Join(sessionsDir, sessionID)

		if _, err := os.Stat(sessionPath); err == nil {
			continue
		}

		if err := os.Mkdir(sessionPath, constants.DirPerm); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, errors.WrapError("create session directory", err)
		}

		session := &Session{Path: sessionPath, SessionsDir: sessionsDir}
		if systemPrompt != "" {
			if err := saveSystemMessage(session, systemPrompt); err != nil && log != nil {
				log.Debugf("Failed to save system message: %v", err)
			}
		}

		return session, nil
	}

	return nil, fmt.Errorf("failed to create session after 100 attempts: too many collisions")
}

// LoadSession loads an existing session by path.
func (m *Manager) LoadSession(sessionPath string) (*Session, error) {
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(m.SessionsDir(), sessionPath)
	}

	info, err := os.Stat(sessionPath)
	if err != nil {
		return nil, errors.WrapError("find session", err)
	}
	if !info.IsDir() {
		return nil, errors.WrapError("validate session path", stdErrors.New("session path is not a directory: "+sessionPath))
	}

	return &Session{Path: sessionPath, SessionsDir: m.SessionsDir()}, nil
}
