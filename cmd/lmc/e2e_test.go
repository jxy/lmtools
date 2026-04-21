//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// E2ETestServer wraps the mock server for e2e tests
type E2ETestServer struct {
	*httptest.Server
	mu           sync.Mutex
	requestCount int
	requests     []interface{}
}

func (e2e *E2ETestServer) recordRequest(req interface{}) int {
	e2e.mu.Lock()
	defer e2e.mu.Unlock()
	e2e.requestCount++
	e2e.requests = append(e2e.requests, req)
	return e2e.requestCount
}

func extractE2EMessageText(req map[string]interface{}) string {
	messages, ok := req["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return ""
	}

	lastMsg, ok := messages[len(messages)-1].(map[string]interface{})
	if !ok {
		return ""
	}

	switch content := lastMsg["content"].(type) {
	case string:
		return content
	case []interface{}:
		for _, block := range content {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if blockType, _ := blockMap["type"].(string); blockType != "text" {
				continue
			}
			if text, ok := blockMap["text"].(string); ok {
				return text
			}
		}
	}

	return ""
}

func contextualE2EResponse(lastMsg string, requestNum int) string {
	switch {
	case strings.Contains(strings.ToLower(lastMsg), "weather"):
		return "Today is sunny with a high of 75°F."
	case strings.Contains(strings.ToLower(lastMsg), "hello"):
		return "Hello! How can I assist you today?"
	case strings.Contains(lastMsg, "continue"):
		return fmt.Sprintf("Continuing conversation #%d...", requestNum)
	case strings.Contains(strings.ToLower(lastMsg), "test"):
		return "Test response confirmed."
	case lastMsg != "":
		return fmt.Sprintf("Response #%d to: %s", requestNum, lastMsg)
	default:
		return "Default response"
	}
}

func writeE2EOpenAIResponse(w http.ResponseWriter, model, response string, requestNum int) {
	resp := map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-e2e-%d", requestNum),
		"object":  "chat.completion",
		"created": 1234567890,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": response,
				},
				"finish_reason": "stop",
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeE2EAnthropicResponse(w http.ResponseWriter, model, response string, requestNum int) {
	resp := map[string]interface{}{
		"id":    fmt.Sprintf("msg-e2e-%d", requestNum),
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": response,
			},
		},
		"stop_reason": "end_turn",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeE2EOpenAIStream(w http.ResponseWriter, model, response string, requestNum int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	chunks := []string{
		fmt.Sprintf(`data: {"id":"chatcmpl-e2e-%d","object":"chat.completion.chunk","created":1234567890,"model":"%s","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, requestNum, model),
		fmt.Sprintf(`data: {"id":"chatcmpl-e2e-%d","object":"chat.completion.chunk","created":1234567890,"model":"%s","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, requestNum, model, response),
		fmt.Sprintf(`data: {"id":"chatcmpl-e2e-%d","object":"chat.completion.chunk","created":1234567890,"model":"%s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, requestNum, model),
		`data: [DONE]`,
	}

	for _, chunk := range chunks {
		fmt.Fprintf(w, "%s\n\n", chunk)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func writeE2EAnthropicStream(w http.ResponseWriter, model, response string, requestNum int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	events := []string{
		fmt.Sprintf(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg-e2e-%d","type":"message","role":"assistant","model":"%s","content":[]}}`, requestNum, model),
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		fmt.Sprintf(`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, response),
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}

	for _, event := range events {
		fmt.Fprintf(w, "%s\n\n", event)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// newE2ETestServer creates a more sophisticated mock server for e2e tests
func newE2ETestServer(t *testing.T) *E2ETestServer {
	e2e := &E2ETestServer{
		requests: make([]interface{}, 0),
	}

	mux := http.NewServeMux()

	handleChat := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		requestNum := e2e.recordRequest(req)
		model, _ := req["model"].(string)
		response := contextualE2EResponse(extractE2EMessageText(req), requestNum)

		switch r.URL.Path {
		case "/v1/chat/completions":
			if stream, _ := req["stream"].(bool); stream {
				writeE2EOpenAIStream(w, model, "This is a streaming response.", requestNum)
				return
			}
			writeE2EOpenAIResponse(w, model, response, requestNum)
		case "/v1/messages":
			if stream, _ := req["stream"].(bool); stream {
				writeE2EAnthropicStream(w, model, "This is a streaming response.", requestNum)
				return
			}
			writeE2EAnthropicResponse(w, model, response, requestNum)
		default:
			resp := map[string]interface{}{
				"response": response,
				"model":    model,
				"id":       fmt.Sprintf("resp-%d", requestNum),
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Logf("encode chat resp: %v", err)
			}
		}
	}

	handleEmbed := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		e2e.recordRequest(req)

		model, _ := req["model"].(string)
		embedding := make([]float64, 1536)
		for i := range embedding {
			embedding[i] = float64(i%100) / 100.0
		}

		resp := map[string]interface{}{
			"embedding": [][]float64{embedding},
			"model":     model,
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("encode embed resp: %v", err)
		}
	}

	handleLegacyStream := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		e2e.recordRequest(req)

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, "This is a streaming response.")
		fmt.Fprintln(w)
	}

	mux.HandleFunc("/chat/", handleChat)
	mux.HandleFunc("/api/v1/resource/chat/", handleChat)
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/v1/messages", handleChat)

	mux.HandleFunc("/embed/", handleEmbed)
	mux.HandleFunc("/api/v1/resource/embed/", handleEmbed)

	mux.HandleFunc("/streamchat/", handleLegacyStream)
	mux.HandleFunc("/api/v1/resource/streamchat/", handleLegacyStream)

	e2e.Server = httptest.NewServer(mux)
	t.Cleanup(e2e.Close)

	return e2e
}

// TestE2E_BasicConversationFlow tests a complete conversation flow
func TestE2E_BasicConversationFlow(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Setup environment
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create custom sessions directory
	sessionsDir := t.TempDir()

	// Start mock server
	server := newE2ETestServer(t)

	// Test 1: Create new session
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "alice", "-model", "gpt4o", "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"Hello, this is my first message")
	if err != nil {
		t.Fatalf("Failed to create session: %v\nStderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Hello! How can I assist you today?") {
		t.Errorf("Expected greeting response, got: %s", stdout)
	}

	// Get session ID using -show-sessions
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "alice", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	// Extract session ID
	sessionID := extractFirstSessionID(stdout)
	if sessionID == "" {
		t.Fatal("Failed to extract session ID from show-sessions output")
	}

	t.Logf("Created session: %s", sessionID)

	// Test 2: Resume session
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "alice", "-model", "gpt4o", "-resume", sessionID, "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"What's the weather like?")
	if err != nil {
		t.Fatalf("Failed to resume session: %v", err)
	}

	if !strings.Contains(stdout, "sunny") {
		t.Errorf("Expected weather response, got: %s", stdout)
	}

	// Test 3: Show conversation
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "alice", "-show", sessionID, "-sessions-dir", sessionsDir},
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
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)

	// Create initial conversation
	_, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "bob", "-model", "gpt4o", "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"Initial message")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "bob", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	sessionID := extractFirstSessionID(stdout)

	// Add second message
	_, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "bob", "-model", "gpt4o", "-resume", sessionID, "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"Second message")
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	// Branch from first message (stdout not used)
	_, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "bob", "-model", "gpt4o", "-branch", sessionID + "/0001", "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"Alternative second message")
	if err != nil {
		t.Fatalf("Failed to create branch: %v", err)
	}

	// Check if we're in a sibling branch
	// stderr is not deterministically used here; rely on output validation
	// if strings.Contains(stderr, "sibling branch") {
	t.Log("Successfully created sibling branch")
	// }

	// Show sessions to see the tree; capture new output for validation
	treeOut, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "bob", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	if !strings.Contains(treeOut, sessionID) {
		t.Errorf("Expected session tree to contain %s, got:\n%s", sessionID, treeOut)
	}
}

// TestE2E_EmbeddingMode tests embedding functionality
func TestE2E_EmbeddingMode(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	server := newE2ETestServer(t)

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "charlie", "-model", "v3large", "-e", "-provider-url", server.URL},
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
	sessionsDir := filepath.Join(tmpHome, ".lmc", "sessions")
	if _, err := os.Stat(sessionsDir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(sessionsDir)
		if len(entries) > 0 {
			t.Error("Sessions were created in embed mode")
		}
	}
}

// TestE2E_StreamingMode tests streaming functionality
func TestE2E_StreamingMode(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)

	// Use test helper to capture streaming output and log directory
	logDir := t.TempDir()
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "dave", "-model", "gpt4o", "-stream", "-provider-url", server.URL, "-sessions-dir", sessionsDir, "-log-dir", logDir},
		"Test streaming response")
	if err != nil {
		t.Fatalf("Failed to run streaming command: %v\nStderr: %s", err, stderr)
	}

	// lmc should print the streamed response text regardless of whether the
	// upstream Argo path is legacy plain text or native SSE.
	outputStr := stdout
	if !strings.Contains(outputStr, "This is a streaming response.") {
		t.Errorf("Expected streaming text output, got: %s", outputStr)
	}

	// Check for stream_chat_output log file
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	streamLogFound := false
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "stream_chat_output") && strings.HasSuffix(entry.Name(), ".log") {
			streamLogFound = true
			// Read and verify log content
			logPath := filepath.Join(logDir, entry.Name())
			content, err := os.ReadFile(logPath)
			if err != nil {
				t.Errorf("Failed to read stream log: %v", err)
			} else if len(content) == 0 {
				t.Error("Stream log file is empty")
			} else {
				t.Logf("Stream log contains %d bytes", len(content))
			}
			break
		}
	}

	if !streamLogFound {
		t.Error("stream_chat_output log file not found")
	}

	// Verify session was created with assistant message
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "dave", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	// Extract session ID
	sessionID := extractFirstSessionID(stdout)

	if sessionID == "" {
		t.Fatal("No session created for streaming response")
	}

	// Show the session to verify assistant message
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "dave", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show session: %v", err)
	}

	// Verify both user and assistant messages are present
	if !strings.Contains(stdout, "Test streaming response") {
		t.Error("User message not found in session")
	}
	if !strings.Contains(stdout, "This is a streaming response.") {
		t.Error("Assistant response not found in session")
	}
	if !strings.Contains(stdout, "[assistant/gpt4o]") {
		t.Error("Model information not recorded in assistant message")
	}
}

// TestE2E_SessionDeletion tests deletion functionality
func TestE2E_SessionDeletion(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)

	// Create a session
	_, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "eve", "-model", "gpt4o", "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"Message to be deleted")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "eve", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	sessionID := extractFirstSessionID(stdout)

	// Verify session exists
	_, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "eve", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show session: %v", err)
	}

	// Delete the session
	_, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "eve", "-delete", sessionID, "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}

	// Verify session is gone
	_, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "eve", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")

	if err == nil {
		t.Error("Expected error showing deleted session, but got none")
	}
}

// TestE2E_ConcurrentOperations tests concurrent session operations
func TestE2E_ConcurrentOperations(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	server := newE2ETestServer(t)

	// Create initial session
	_, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "frank", "-model", "gpt4o", "-provider-url", server.URL, "-sessions-dir", sessionsDir},
		"Start concurrent test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Get session ID using -show-sessions
	stdout, _, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "frank", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	sessionID := extractFirstSessionID(stdout)

	// Run multiple concurrent resumes
	done := make(chan struct{})
	errors := make(chan error, 3)

	for i := 0; i < 3; i++ {
		go func(id int) {
			_, _, err := runLmcCommand(t, lmcBin,
				[]string{"-argo-user", "frank", "-model", "gpt4o", "-resume", sessionID, "-provider-url", server.URL, "-sessions-dir", sessionsDir},
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
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "frank", "-show", sessionID, "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show session: %v", err)
	}

	// Count messages by looking for role headers in the new format
	msgLines := strings.Split(stdout, "\n")
	var messageCount int
	for _, line := range msgLines {
		// Look for lines with format: "[user] timestamp" or "[assistant/model] timestamp"
		if strings.HasPrefix(line, "[user]") || strings.HasPrefix(line, "[assistant") {
			messageCount++
		}
	}
	t.Logf("Total messages after concurrent operations: %d", messageCount)

	if messageCount < 4 { // Initial + 3 concurrent
		t.Errorf("Expected at least 4 messages, got %d", messageCount)
	}
}
