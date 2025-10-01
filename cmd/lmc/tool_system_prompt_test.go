//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"io"
	"lmtools/internal/mockserver"
	"lmtools/internal/prompts"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestToolSystemPromptUsage verifies that the correct system prompt is used
// when the -tool flag is specified without an explicit -s flag
func TestToolSystemPromptUsage(t *testing.T) {
	// Start a mock server for all tests
	ms := mockserver.NewMockServer()
	defer ms.Close()
	
	// Test 1: With -tool flag, no explicit -s flag - should use ToolSystemPrompt
	t.Run("tool_without_explicit_system", func(t *testing.T) {
		lmcBin := getLmcBinary(t)
		logDir := t.TempDir()
		sessDir := t.TempDir()
		
		// Run lmc with -tool but without -s
		// Use mock server to avoid actual API call
		_, stderr, err := runLmcCommandWithSpecificLogDir(t, lmcBin, 
			[]string{
				"-provider", "openai",
				"-provider-url", ms.URL() + "/v1",
				"-tool",
				"-sessions-dir", sessDir,
			}, 
			"test input", logDir)
		
		// Command should succeed with mock server
		if err != nil {
			t.Logf("Command failed (might be expected): %v", err)
		}
		t.Logf("stderr: %s", stderr)
		
		// Find and read the chat_input JSON file
		jsonFile := findLogFile(t, logDir, "chat_input")
		if jsonFile == "" {
			t.Fatal("Could not find chat_input log file")
		}
		
		data := readJSONFile(t, jsonFile)
		
		// Extract and verify system prompt
		systemPrompt := extractSystemPrompt(t, data)
		t.Logf("System prompt found: %q", systemPrompt)
		
		// Should contain the tool-specific prompt text
		if !strings.Contains(systemPrompt, "universal_command tool") {
			t.Errorf("Expected ToolSystemPrompt containing 'universal_command tool', got: %s", systemPrompt)
		}
		
		// Should NOT be just the default prompt
		if systemPrompt == prompts.DefaultSystemPrompt {
			t.Errorf("Got DefaultSystemPrompt when ToolSystemPrompt was expected")
		}
	})
	
	// Test 2: With -tool and explicit -s flag - should use custom prompt
	t.Run("tool_with_explicit_system", func(t *testing.T) {
		lmcBin := getLmcBinary(t)
		logDir := t.TempDir()
		sessDir := t.TempDir()
		customPrompt := "My custom system prompt for testing"
		
		_, stderr, err := runLmcCommandWithSpecificLogDir(t, lmcBin, 
			[]string{
				"-provider", "openai",
				"-provider-url", ms.URL() + "/v1",
				"-tool",
				"-s", customPrompt,
				"-sessions-dir", sessDir,
			}, 
			"test input", logDir)
		
		if err != nil {
			t.Logf("Command failed (might be expected): %v", err)
		}
		t.Logf("stderr: %s", stderr)
		
		jsonFile := findLogFile(t, logDir, "chat_input")
		if jsonFile == "" {
			t.Fatal("Could not find chat_input log file")
		}
		
		data := readJSONFile(t, jsonFile)
		systemPrompt := extractSystemPrompt(t, data)
		t.Logf("System prompt found: %q", systemPrompt)
		
		// Should use the custom prompt, not ToolSystemPrompt
		if systemPrompt != customPrompt {
			t.Errorf("Expected custom prompt %q, got: %q", customPrompt, systemPrompt)
		}
	})
	
	// Test 3: Session resume with -tool flag - should use ToolSystemPrompt  
	t.Run("resume_session_with_tool", func(t *testing.T) {
		lmcBin := getLmcBinary(t)
		logDir := t.TempDir()
		sessDir := t.TempDir()
		
		// First, create a session without -tool flag
		stdout, stderr, err := runLmcCommandWithSpecificLogDir(t, lmcBin,
			[]string{
				"-provider", "openai",
				"-provider-url", ms.URL() + "/v1",
				"-sessions-dir", sessDir,
			},
			"initial message", logDir)

		if err != nil {
			t.Fatalf("Failed to create initial session: %v\nStdout: %s\nStderr: %s", err, stdout, stderr)
		}

		// Wait for session to be created
		sessionDir := filepath.Join(sessDir, "0001")
		if !waitForFile(t, sessionDir, time.Second) {
			// List the contents of sessDir to debug
			entries, _ := os.ReadDir(sessDir)
			t.Logf("Contents of sessions directory %s:", sessDir)
			for _, entry := range entries {
				t.Logf("  - %s", entry.Name())
			}
			t.Fatal("Session directory was not created within timeout")
		}

		// Now check if there are message files in the session directory
		entries, err := os.ReadDir(sessionDir)
		if err != nil {
			t.Fatalf("Failed to read session directory: %v", err)
		}

		hasMessages := false
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") && !strings.Contains(entry.Name(), ".tools.json") {
				hasMessages = true
				break
			}
		}

		if !hasMessages {
			t.Fatal("No message files found in session directory")
		}
		
		// Now resume the session with -tool flag
		_, stderr2, err2 := runLmcCommandWithSpecificLogDir(t, lmcBin,
			[]string{
				"-provider", "openai", 
				"-provider-url", ms.URL() + "/v1",
				"-tool",
				"-resume", "0001",
				"-sessions-dir", sessDir,
			},
			"follow-up message", logDir)

		if err2 != nil {
			t.Logf("Command failed (might be expected): %v", err2)
		}
		t.Logf("stderr: %s", stderr2)
		
		// Find the second request's JSON file
		logEntries, _ := os.ReadDir(logDir)
		var jsonFiles []string
		for _, entry := range logEntries {
			if strings.Contains(entry.Name(), "chat_input") && strings.HasSuffix(entry.Name(), ".json") {
				jsonFiles = append(jsonFiles, filepath.Join(logDir, entry.Name()))
			}
		}
		
		if len(jsonFiles) < 2 {
			t.Fatalf("Expected at least 2 JSON files, found %d", len(jsonFiles))
		}
		
		// Check the second request (the resume with -tool)
		data := readJSONFile(t, jsonFiles[1])
		systemPrompt := extractSystemPrompt(t, data)
		t.Logf("System prompt in resumed session: %q", systemPrompt)
		
		// When resuming with -tool, it should fork and use ToolSystemPrompt
		if !strings.Contains(systemPrompt, "universal_command tool") {
			t.Errorf("Expected ToolSystemPrompt when resuming with -tool, got: %s", systemPrompt)
		}
	})
	
	// Test 4: Without -tool flag - should use DefaultSystemPrompt
	t.Run("no_tool_flag", func(t *testing.T) {
		lmcBin := getLmcBinary(t)
		logDir := t.TempDir()
		sessDir := t.TempDir()
		
		_, stderr, err := runLmcCommandWithSpecificLogDir(t, lmcBin, 
			[]string{
				"-provider", "openai",
				"-provider-url", ms.URL() + "/v1",
				"-sessions-dir", sessDir,
			}, 
			"test input", logDir)
		
		if err != nil {
			t.Logf("Command failed (might be expected): %v", err)
		}
		t.Logf("stderr: %s", stderr)
		
		jsonFile := findLogFile(t, logDir, "chat_input")
		if jsonFile == "" {
			t.Fatal("Could not find chat_input log file")
		}
		
		data := readJSONFile(t, jsonFile)
		systemPrompt := extractSystemPrompt(t, data)
		t.Logf("System prompt found: %q", systemPrompt)
		
		// Should use the default prompt
		if systemPrompt != prompts.DefaultSystemPrompt {
			t.Errorf("Expected DefaultSystemPrompt %q, got: %q", prompts.DefaultSystemPrompt, systemPrompt)
		}
	})
}

// Helper functions

// findLogFile searches for a log file with the given prefix in the directory
func findLogFile(t *testing.T, dir, prefix string) string {
	t.Helper()

	// Wait for log file to be written
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Look for files that contain the prefix in their name and end with .json
		for _, entry := range entries {
			if strings.Contains(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".json") {
				return filepath.Join(dir, entry.Name())
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// If we get here, file wasn't found - list files for debugging
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	t.Logf("Files in log directory %s:", dir)
	for _, entry := range entries {
		t.Logf("  - %s", entry.Name())
	}
	
	return ""
}

// readJSONFile reads and unmarshals a JSON file
func readJSONFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Failed to open JSON file %s: %v", path, err)
	}
	defer file.Close()
	
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("Failed to read JSON file: %v", err)
	}
	
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	
	return result
}

// extractSystemPrompt extracts the system prompt from the request JSON
func extractSystemPrompt(t *testing.T, data map[string]interface{}) string {
	t.Helper()
	
	// The system prompt can be in different places depending on the provider
	// Check common locations
	
	// Try direct "system" field (used by some providers)
	if system, ok := data["system"].(string); ok {
		return system
	}
	
	// Try messages array for system message (Anthropic format)
	if messages, ok := data["messages"].([]interface{}); ok {
		for _, msg := range messages {
			msgMap, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			
			role, _ := msgMap["role"].(string)
			if role == "system" {
				if content, ok := msgMap["content"].(string); ok {
					return content
				}
				// Handle array content format
				if contentArray, ok := msgMap["content"].([]interface{}); ok {
					for _, c := range contentArray {
						if cMap, ok := c.(map[string]interface{}); ok {
							if text, ok := cMap["text"].(string); ok {
								return text
							}
						}
					}
				}
			}
		}
	}
	
	t.Logf("Full JSON structure: %+v", data)
	return ""
}