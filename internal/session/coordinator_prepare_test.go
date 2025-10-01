package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockConfig implements core.RequestConfig for testing
type mockConfig struct {
	resume              string
	branch              string
	effectiveSystem     string
	sessionsDir         string
	systemExplicitlySet bool // Add this field to control forking behavior
}

func (m *mockConfig) GetResume() string          { return m.resume }
func (m *mockConfig) GetBranch() string          { return m.branch }
func (m *mockConfig) GetEffectiveSystem() string { return m.effectiveSystem }
func (m *mockConfig) GetSessionsDir() string     { return m.sessionsDir }

// Implement other required methods with defaults
func (m *mockConfig) GetModel() string                 { return "test-model" }
func (m *mockConfig) GetProvider() string              { return "test" }
func (m *mockConfig) GetProviderURL() string           { return "" }
func (m *mockConfig) GetAPIKeyFile() string            { return "" }
func (m *mockConfig) GetStream() bool                  { return false }
func (m *mockConfig) GetMaxRetries() int               { return 3 }
func (m *mockConfig) GetRetryDelay() time.Duration     { return time.Second }
func (m *mockConfig) GetRequestTimeout() time.Duration { return 30 * time.Second }
func (m *mockConfig) GetConnectTimeout() time.Duration { return 10 * time.Second }
func (m *mockConfig) GetLogLevel() string              { return "info" }
func (m *mockConfig) GetLogDir() string                { return "" }
func (m *mockConfig) GetToolEnabled() bool             { return false }
func (m *mockConfig) GetToolTimeout() time.Duration    { return 5 * time.Minute }
func (m *mockConfig) GetToolAutoApprove() bool         { return false }
func (m *mockConfig) GetToolWhitelist() string         { return "" }
func (m *mockConfig) GetToolBlacklist() string         { return "" }
func (m *mockConfig) GetToolNonInteractive() bool      { return false }
func (m *mockConfig) GetToolMaxOutputBytes() int       { return 1048576 }
func (m *mockConfig) GetMaxToolRounds() int            { return 10 }
func (m *mockConfig) GetMaxToolParallel() int          { return 4 }
func (m *mockConfig) GetArgoUser() string              { return "" }
func (m *mockConfig) GetEmbedding() bool               { return false }
func (m *mockConfig) GetShowThinking() bool            { return false }
func (m *mockConfig) GetUser() string                  { return "test-user" }
func (m *mockConfig) GetEnv() string                   { return "test" }
func (m *mockConfig) IsEmbed() bool                    { return false }
func (m *mockConfig) IsStreamChat() bool               { return false }
func (m *mockConfig) IsToolEnabled() bool              { return false }
func (m *mockConfig) IsSystemExplicitlySet() bool      { return m.systemExplicitlySet }
func (m *mockConfig) GetShowSessions() bool            { return false }
func (m *mockConfig) GetRegenerateFromEnd() bool       { return false }
func (m *mockConfig) GetTemperature() *float64         { return nil }
func (m *mockConfig) GetMaxTokens() *int               { return nil }
func (m *mockConfig) GetTopP() *float64                { return nil }
func (m *mockConfig) GetTopK() *int                    { return nil }
func (m *mockConfig) GetStopSequences() []string       { return nil }
func (m *mockConfig) GetDebug() bool                   { return false }
func (m *mockConfig) GetHTTPRetries() int              { return 3 }
func (m *mockConfig) GetHTTPRetryDelay() time.Duration { return time.Second }
func (m *mockConfig) GetHTTPRetryBackoff() float64     { return 2.0 }
func (m *mockConfig) GetHTTPRetryJitter() float64      { return 0.1 }

// mockNotifier implements core.Notifier for testing
type mockNotifier struct {
	messages []string
}

func (m *mockNotifier) Infof(format string, args ...interface{}) {
	m.messages = append(m.messages, "INFO: "+format)
}

func (m *mockNotifier) Warnf(format string, args ...interface{}) {
	m.messages = append(m.messages, "WARN: "+format)
}

func (m *mockNotifier) Errorf(format string, args ...interface{}) {
	m.messages = append(m.messages, "ERROR: "+format)
}

func (m *mockNotifier) Debugf(format string, args ...interface{}) {
	m.messages = append(m.messages, "DEBUG: "+format)
}

func (m *mockNotifier) Promptf(format string, args ...interface{}) {
	m.messages = append(m.messages, "PROMPT: "+format)
}

// mockApprover implements core.Approver for testing
type mockApprover struct {
	shouldApprove bool
}

func (m *mockApprover) Approve(ctx context.Context, command []string) (bool, error) {
	return m.shouldApprove, nil
}

// TestCoordinatorPrepareSessionNewSession tests creating a new session
func TestCoordinatorPrepareSessionNewSession(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_new_session_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &mockConfig{
		effectiveSystem: "You are a helpful assistant",
		sessionsDir:     tmpDir,
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	// Initialize logger for the test
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	coordinator := NewCoordinator(cfg, notifier)

	// Test creating new session
	result, err := coordinator.PrepareSession(ctx, "Hello, world!", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}
	if result.ExecutedPending {
		t.Error("Expected ExecutedPending=false for new session")
	}

	// Verify session was created with system prompt
	// Verify system prompt was set correctly by checking the first message
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}
	if len(messages) > 0 && messages[0].Role == core.RoleSystem {
		if messages[0].Content != cfg.effectiveSystem {
			t.Errorf("Expected system=%q, got %q", cfg.effectiveSystem, messages[0].Content)
		}
	}

	// Verify user message was saved
	messages, err = GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages (system + user), got %d", len(messages))
	}
	if messages[0].Role != core.RoleSystem || messages[0].Content != cfg.effectiveSystem {
		t.Errorf("Unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != core.RoleUser || messages[1].Content != "Hello, world!" {
		t.Errorf("Unexpected user message: %+v", messages[1])
	}
}

// TestCoordinatorPrepareSessionResume tests resuming an existing session
func TestCoordinatorPrepareSessionResume(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_resume_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize logger
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create an existing session
	existingSession, err := CreateSession("Original system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message to the session
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "First message",
		Timestamp: time.Now(),
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, userMsg, nil, nil); err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	cfg := &mockConfig{
		resume:          GetSessionID(existingSession.Path),
		effectiveSystem: "Original system prompt", // Same system prompt
		sessionsDir:     filepath.Dir(existingSession.Path),
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	coordinator := NewCoordinator(cfg, notifier)

	// Test resuming session
	result, err := coordinator.PrepareSession(ctx, "Second message", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}
	if result.ExecutedPending {
		t.Error("Expected ExecutedPending=false (no pending tools)")
	}

	// Verify same session path (no fork)
	if result.Session.Path != existingSession.Path {
		t.Errorf("Expected same session path, got %s", result.Session.Path)
	}

	// Verify messages
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages (system + 2 user), got %d", len(messages))
	}
	if messages[2].Content != "Second message" {
		t.Errorf("Expected second message content='Second message', got %q", messages[2].Content)
	}
}

// TestCoordinatorPrepareSessionForkOnSystemChange tests forking when system prompt changes
func TestCoordinatorPrepareSessionForkOnSystemChange(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_fork_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize logger
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create an existing session
	existingSession, err := CreateSession("Original system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message to the session
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "First message",
		Timestamp: time.Now(),
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, userMsg, nil, nil); err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	cfg := &mockConfig{
		resume:              GetSessionID(existingSession.Path),
		effectiveSystem:     "Different system prompt", // Changed system prompt
		sessionsDir:         filepath.Dir(existingSession.Path),
		systemExplicitlySet: true, // System prompt was explicitly set via -s flag
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	coordinator := NewCoordinator(cfg, notifier)

	// Test resuming with different system prompt
	result, err := coordinator.PrepareSession(ctx, "Second message", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify forked to new session
	if result.Session.Path == existingSession.Path {
		t.Error("Expected different session path after fork")
	}

	// Verify fork notification
	foundForkNotification := false
	for _, msg := range notifier.messages {
		if msg == "INFO: Forked session due to system prompt change: %s" {
			foundForkNotification = true
			break
		}
	}
	if !foundForkNotification {
		t.Error("Expected fork notification in notifier messages")
	}

	// Verify new session has new system prompt
	// Verify system prompt was set correctly by checking the first message
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}
	if len(messages) > 0 && messages[0].Role == core.RoleSystem {
		if messages[0].Content != cfg.effectiveSystem {
			t.Errorf("Expected system=%q, got %q", cfg.effectiveSystem, messages[0].Content)
		}
	}

	// Verify messages were copied
	messages, err = GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages in forked session (new system + copied user + new user), got %d", len(messages))
	}
}

// TestCoordinatorPrepareSessionBranch tests explicit branching
func TestCoordinatorPrepareSessionBranch(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_branch_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize logger
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create an existing session
	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add messages
	msg1 := Message{Role: core.RoleUser, Content: "Message 1", Timestamp: time.Now()}
	res1, err := AppendMessageWithToolInteraction(context.Background(), existingSession, msg1, nil, nil)
	if err != nil {
		t.Fatalf("Failed to append message 1: %v", err)
	}

	msg2 := Message{Role: core.RoleAssistant, Content: "Response 1", Timestamp: time.Now()}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, msg2, nil, nil); err != nil {
		t.Fatalf("Failed to append message 2: %v", err)
	}

	cfg := &mockConfig{
		branch:          GetSessionID(existingSession.Path) + "/" + res1.MessageID,
		effectiveSystem: "System prompt",
		sessionsDir:     filepath.Dir(existingSession.Path),
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	coordinator := NewCoordinator(cfg, notifier)

	// Test branching from first message
	result, err := coordinator.PrepareSession(ctx, "Alternative message 2", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify branched to sibling
	if result.Session.Path == existingSession.Path {
		t.Error("Expected different session path after branch")
	}

	// Verify sibling path format
	expectedPrefix := existingSession.Path + "/" + res1.MessageID + ".s."
	if !strings.HasPrefix(result.Session.Path, expectedPrefix) {
		t.Errorf("Expected sibling path to start with %s, got %s", expectedPrefix, result.Session.Path)
	}

	// Verify only first message was copied
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// Debug: log the messages
	for i, msg := range messages {
		t.Logf("Message %d: Role=%s, Content=%q", i, msg.Role, msg.Content)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message in branch (new alternative message), got %d", len(messages))
	}
	if messages[0].Content != "Alternative message 2" {
		t.Errorf("Expected message content='Alternative message 2', got %q", messages[0].Content)
	}
}

// TestCoordinatorPrepareSessionPendingTools tests executing pending tools on resume
func TestCoordinatorPrepareSessionPendingTools(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_pending_tools_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize logger
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create an existing session
	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message with pending tool calls
	assistantMsg := Message{
		Role:      core.RoleAssistant,
		Content:   "I'll help you with that",
		Timestamp: time.Now(),
	}
	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{
				ID:   "call_123",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["echo","hello"]}`),
			},
		},
	}
	if _, err := AppendMessageWithToolInteraction(ctx, existingSession, assistantMsg, toolInteraction.Calls, toolInteraction.Results); err != nil {
		t.Fatalf("Failed to append message with tools: %v", err)
	}

	// Create a config that enables tools
	cfg := &mockConfigWithTools{
		mockConfig: mockConfig{
			resume:          GetSessionID(existingSession.Path),
			effectiveSystem: "System prompt",
			sessionsDir:     filepath.Dir(existingSession.Path),
		},
		toolEnabled: true,
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	coordinator := NewCoordinator(cfg, notifier)

	// Test resuming with pending tools
	result, err := coordinator.PrepareSession(ctx, "", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}
	if !result.ExecutedPending {
		t.Error("Expected ExecutedPending=true when pending tools found")
	}

	// Note: Actual tool execution would require more setup with the executor
	// This test verifies that pending tools are detected and the flag is set
}

// TestCoordinatorPrepareSessionRegeneration tests regeneration (no user message saved)
func TestCoordinatorPrepareSessionRegeneration(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_regeneration_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize logger
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create an existing session
	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "First message",
		Timestamp: time.Now(),
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, userMsg, nil, nil); err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	cfg := &mockConfig{
		resume:          GetSessionID(existingSession.Path),
		effectiveSystem: "System prompt",
		sessionsDir:     filepath.Dir(existingSession.Path),
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	coordinator := NewCoordinator(cfg, notifier)

	// Test regeneration (isRegeneration=true)
	result, err := coordinator.PrepareSession(ctx, "This should not be saved", true, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify no new message was saved
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages (system + first user, regeneration shouldn't save new input), got %d", len(messages))
	}
}

// TestCoordinatorPrepareSessionEmptyInput tests handling empty input
func TestCoordinatorPrepareSessionEmptyInput(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "coordinator_empty_input_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize logger
	ctx := context.Background()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tmpDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	cfg := &mockConfig{
		effectiveSystem: "System prompt",
		sessionsDir:     tmpDir,
	}
	notifier := &mockNotifier{}
	approver := &mockApprover{shouldApprove: true}

	coordinator := NewCoordinator(cfg, notifier)

	// Test with empty input
	result, err := coordinator.PrepareSession(ctx, "", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify no message was saved
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("Expected 1 message (system prompt only) for empty input, got %d", len(messages))
	}
	if len(messages) > 0 && messages[0].Role != core.RoleSystem {
		t.Errorf("Expected system message, got %v", messages[0].Role)
	}
}

// mockConfigWithTools extends mockConfig to support tool configuration
type mockConfigWithTools struct {
	mockConfig
	toolEnabled bool
}

func (m *mockConfigWithTools) GetToolEnabled() bool { return m.toolEnabled }
