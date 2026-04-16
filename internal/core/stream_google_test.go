package core

import (
	"sync"
	"testing"
)

// TestGoogleStreamStateGenerateToolCallID tests that the stream state generates unique IDs
func TestGoogleStreamStateGenerateToolCallID(t *testing.T) {
	// Create a new stream state
	state := &GoogleStreamState{}

	// Test sequential generation
	id1 := state.generateToolCallID()
	id2 := state.generateToolCallID()
	id3 := state.generateToolCallID()

	if id1 == id2 || id2 == id3 || id1 == id3 {
		t.Errorf("generateToolCallID produced duplicate IDs: %s, %s, %s", id1, id2, id3)
	}

	// Test format
	if id1 != "call_1" {
		t.Errorf("First ID should be 'call_1', got %s", id1)
	}
	if id2 != "call_2" {
		t.Errorf("Second ID should be 'call_2', got %s", id2)
	}
}

// TestGoogleToolCallIDGeneratorConcurrency tests thread-safety of the parse function ID generator
func TestGoogleToolCallIDGeneratorConcurrency(t *testing.T) {
	const numGoroutines = 100
	const numCallsPerGoroutine = 100

	// Collect all generated IDs
	idChan := make(chan string, numGoroutines*numCallsPerGoroutine)
	var wg sync.WaitGroup

	// Start multiple goroutines generating IDs concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numCallsPerGoroutine; j++ {
				idChan <- generateGoogleToolCallID()
			}
		}()
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(idChan)

	// Check for uniqueness
	seen := make(map[string]bool)
	for id := range idChan {
		if seen[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		seen[id] = true
	}

	// Verify we got the expected number of unique IDs
	if len(seen) != numGoroutines*numCallsPerGoroutine {
		t.Errorf("Expected %d unique IDs, got %d", numGoroutines*numCallsPerGoroutine, len(seen))
	}
}

func TestGoogleStreamStateParseLinePreservesThoughtSignature(t *testing.T) {
	state := &GoogleStreamState{}
	line := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"weather"}},"thoughtSignature":"sig-123"}]}}]}`

	_, calls, done, err := state.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine() error = %v", err)
	}
	if done {
		t.Fatal("ParseLine() done = true, want false")
	}
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if got := calls[0].ThoughtSignature; got != "sig-123" {
		t.Fatalf("thought signature = %q, want %q", got, "sig-123")
	}
}
