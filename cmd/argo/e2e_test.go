//go:build e2e
// +build e2e

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	argo "lmtools/argolib"
)

// E2ETestServer wraps the mock server for e2e tests
type E2ETestServer struct {
	*httptest.Server
	mu           sync.Mutex
	requestCount int
	requests     []interface{}
}

// newE2ETestServer creates a more sophisticated mock server for e2e tests
func newE2ETestServer(t *testing.T) *E2ETestServer {
	e2e := &E2ETestServer{
		requests: make([]interface{}, 0),
	}
	
	mux := http.NewServeMux()
	
	// Handle chat endpoint
	mux.HandleFunc("/chat/", func(w http.ResponseWriter, r *http.Request) {
		var req argo.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		
		e2e.mu.Lock()
		e2e.requestCount++
		e2e.requests = append(e2e.requests, req)
		requestNum := e2e.requestCount
		e2e.mu.Unlock()
		
		// Generate contextual responses
		response := "Default response"
		if len(req.Messages) > 0 {
			lastMsg := req.Messages[len(req.Messages)-1].Content
			switch {
			case strings.Contains(strings.ToLower(lastMsg), "weather"):
				response = "Today is sunny with a high of 75°F."
			case strings.Contains(strings.ToLower(lastMsg), "hello"):
				response = "Hello! How can I assist you today?"
			case strings.Contains(lastMsg, "continue"):
				response = fmt.Sprintf("Continuing conversation #%d...", requestNum)
			case strings.Contains(lastMsg, "test"):
				response = "Test response confirmed."
			default:
				response = fmt.Sprintf("Response #%d to: %s", requestNum, lastMsg)
			}
		}
		
		resp := map[string]interface{}{
			"response": response,
			"model":    req.Model,
			"id":       fmt.Sprintf("resp-%d", requestNum),
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	
	// Handle embedding endpoint
	mux.HandleFunc("/embed/", func(w http.ResponseWriter, r *http.Request) {
		var req argo.EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		
		e2e.mu.Lock()
		e2e.requestCount++
		e2e.requests = append(e2e.requests, req)
		e2e.mu.Unlock()
		
		// Generate dummy embedding (2D array as expected by response handler)
		embedding := make([]float64, 1536)
		for i := range embedding {
			embedding[i] = float64(i%100) / 100.0
		}
		
		resp := map[string]interface{}{
			"embedding": [][]float64{embedding}, // Wrap in outer array
			"model":     req.Model,
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	
	// Handle streaming endpoint
	mux.HandleFunc("/streamchat/", func(w http.ResponseWriter, r *http.Request) {
		var req argo.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		
		e2e.mu.Lock()
		e2e.requestCount++
		e2e.requests = append(e2e.requests, req)
		e2e.mu.Unlock()
		
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		
		// Stream a simple response
		words := []string{"This", "is", "a", "streaming", "response."}
		for _, word := range words {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s \"}}]}\n\n", word)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
		
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	
	e2e.Server = httptest.NewServer(mux)
	t.Cleanup(e2e.Server.Close)
	
	return e2e
}

// TestE2E_BasicConversationFlow tests a complete conversation flow
func TestE2E_BasicConversationFlow(t *testing.T) {
	// Build argo binary
	argoBin := buildArgoBinary(t)
	
	// Setup environment
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	// Create custom sessions directory
	sessionsDir := t.TempDir()
	
	// Start mock server
	server := newE2ETestServer(t)
	
	// Test 1: Create new session
	stdout, stderr, err := runArgoCommand(t, argoBin,
		[]string{"-u", "alice", "-m", "gpt4o", "--env", server.URL, "-sessions-dir", sessionsDir},
		"Hello, this is my first message")
	
	if err != nil {
		t.Fatalf("Failed to create session: %v\nStderr: %s", err, stderr)
	}
	
	if !strings.Contains(stdout, "Hello! How can I assist you today?") {
		t.Errorf("Expected greeting response, got: %s", stdout)
	}
	
	// Get session ID using -show-sessions
	stdout, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "alice", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	// Extract session ID
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, "/ (created:") {
			sessionID = strings.TrimSpace(strings.Split(line, "/")[0])
			break
		}
	}
	
	if sessionID == "" {
		t.Fatal("Failed to extract session ID from show-sessions output")
	}
	
	t.Logf("Created session: %s", sessionID)
	
	// Test 2: Resume session
	stdout, stderr, err = runArgoCommand(t, argoBin,
		[]string{"-u", "alice", "-m", "gpt4o", "-resume", sessionID, "--env", server.URL, "-sessions-dir", sessionsDir},
		"What's the weather like?")
	
	if err != nil {
		t.Fatalf("Failed to resume session: %v", err)
	}
	
	if !strings.Contains(stdout, "sunny") {
		t.Errorf("Expected weather response, got: %s", stdout)
	}
	
	// Test 3: Show conversation
	stdout, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "alice", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show session: %v", err)
	}
	
	// Verify conversation history
	if !strings.Contains(stdout, "Hello, this is my first message") {
		t.Error("Missing first user message in history")
	}
	if !strings.Contains(stdout, "What's the weather like?") {
		t.Error("Missing second user message in history")
	}
	
	// Verify request count
	if server.requestCount != 2 {
		t.Errorf("Expected 2 requests, got %d", server.requestCount)
	}
}

// TestE2E_BranchingConversation tests branching functionality
func TestE2E_BranchingConversation(t *testing.T) {
	argoBin := buildArgoBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)
	
	// Create initial conversation
	_, stderr, err := runArgoCommand(t, argoBin,
		[]string{"-u", "bob", "-m", "gpt4o", "--env", server.URL, "-sessions-dir", sessionsDir},
		"Initial message")
	
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	// Get session ID using -show-sessions
	stdout, _, err := runArgoCommand(t, argoBin,
		[]string{"-u", "bob", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, "/ (created:") {
			sessionID = strings.TrimSpace(strings.Split(line, "/")[0])
			break
		}
	}
	
	// Add second message
	_, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "bob", "-m", "gpt4o", "-resume", sessionID, "--env", server.URL, "-sessions-dir", sessionsDir},
		"Second message")
	
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}
	
	// Branch from first message
	stdout, stderr, err = runArgoCommand(t, argoBin,
		[]string{"-u", "bob", "-m", "gpt4o", "-branch", sessionID + "/0001", "--env", server.URL, "-sessions-dir", sessionsDir},
		"Alternative second message")
	
	if err != nil {
		t.Fatalf("Failed to create branch: %v\nStderr: %s", err, stderr)
	}
	
	// Check if we're in a sibling branch
	if strings.Contains(stderr, "sibling branch") {
		t.Log("Successfully created sibling branch")
	}
	
	// Show sessions to see the tree
	stdout, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "bob", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	t.Logf("Session tree:\n%s", stdout)
}

// TestE2E_EmbeddingMode tests embedding functionality
func TestE2E_EmbeddingMode(t *testing.T) {
	argoBin := buildArgoBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	server := newE2ETestServer(t)
	
	stdout, stderr, err := runArgoCommand(t, argoBin,
		[]string{"-u", "charlie", "-m", "v3large", "-e", "--env", server.URL},
		"Generate an embedding for this text")
	
	if err != nil {
		t.Fatalf("Failed to generate embedding: %v\nStderr: %s", err, stderr)
	}
	
	// Parse embedding output
	var result []float64
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("Failed to parse embedding: %v", err)
	}
	
	if len(result) != 1536 {
		t.Errorf("Expected 1536 dimensions, got %d", len(result))
	}
	
	// Verify no session was created (embed mode auto-disables sessions)
	sessionsDir := filepath.Join(tmpHome, ".argo", "sessions")
	if _, err := os.Stat(sessionsDir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(sessionsDir)
		if len(entries) > 0 {
			t.Error("Sessions were created in embed mode")
		}
	}
}

// TestE2E_StreamingMode tests streaming functionality
func TestE2E_StreamingMode(t *testing.T) {
	argoBin := buildArgoBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	server := newE2ETestServer(t)
	
	// Use exec.Command directly to capture streaming output
	cmd := exec.Command(argoBin, "-u", "dave", "-m", "gpt4o", "--stream", "--env", server.URL)
	cmd.Stdin = strings.NewReader("Test streaming response")
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to run streaming command: %v\nOutput: %s", err, output)
	}
	
	// Verify we got streaming response (check for SSE format)
	outputStr := string(output)
	if !strings.Contains(outputStr, "data:") || !strings.Contains(outputStr, "[DONE]") {
		t.Errorf("Expected streaming response in SSE format, got: %s", outputStr)
	}
}

// TestE2E_SessionDeletion tests deletion functionality
func TestE2E_SessionDeletion(t *testing.T) {
	argoBin := buildArgoBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)
	
	// Create a session
	_, _, err := runArgoCommand(t, argoBin,
		[]string{"-u", "eve", "-m", "gpt4o", "--env", server.URL, "-sessions-dir", sessionsDir},
		"Message to be deleted")
	
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	// Get session ID using -show-sessions
	stdout, _, err := runArgoCommand(t, argoBin,
		[]string{"-u", "eve", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, "/ (created:") {
			sessionID = strings.TrimSpace(strings.Split(line, "/")[0])
			break
		}
	}
	
	// Verify session exists
	_, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "eve", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show session: %v", err)
	}
	
	// Delete the session
	_, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "eve", "-delete", sessionID, "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}
	
	// Verify session is gone
	_, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "eve", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	
	if err == nil {
		t.Error("Expected error showing deleted session, but got none")
	}
}

// TestE2E_ConcurrentOperations tests concurrent session operations
func TestE2E_ConcurrentOperations(t *testing.T) {
	argoBin := buildArgoBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)
	
	// Create initial session
	_, _, err := runArgoCommand(t, argoBin,
		[]string{"-u", "frank", "-m", "gpt4o", "--env", server.URL, "-sessions-dir", sessionsDir},
		"Start concurrent test")
	
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	
	// Get session ID using -show-sessions
	stdout, _, err := runArgoCommand(t, argoBin,
		[]string{"-u", "frank", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}
	
	var sessionID string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if strings.Contains(line, "/ (created:") {
			sessionID = strings.TrimSpace(strings.Split(line, "/")[0])
			break
		}
	}
	
	// Run multiple concurrent resumes
	done := make(chan struct{})
	errors := make(chan error, 3)
	
	for i := 0; i < 3; i++ {
		go func(id int) {
			_, _, err := runArgoCommand(t, argoBin,
				[]string{"-u", "frank", "-m", "gpt4o", "-resume", sessionID, "--env", server.URL, "-sessions-dir", sessionsDir},
				fmt.Sprintf("Concurrent message %d", id))
			if err != nil {
				errors <- err
			}
			done <- struct{}{}
		}(i)
	}
	
	// Wait for all to complete
	for i := 0; i < 3; i++ {
		<-done
	}
	close(errors)
	
	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent operation failed: %v", err)
	}
	
	// Verify all messages were added
	stdout, _, err = runArgoCommand(t, argoBin,
		[]string{"-u", "frank", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	
	if err != nil {
		t.Fatalf("Failed to show session: %v", err)
	}
	
	// Count messages
	messageCount := strings.Count(stdout, "[user]") + strings.Count(stdout, "[assistant]")
	t.Logf("Total messages after concurrent operations: %d", messageCount)
	
	if messageCount < 4 { // Initial + 3 concurrent
		t.Errorf("Expected at least 4 messages, got %d", messageCount)
	}
}