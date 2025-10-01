package logger

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestLoggerConcurrentInitialization tests that concurrent calls to GetLogger and InitializeWithOptions
// don't cause deadlocks
func TestLoggerConcurrentInitialization(t *testing.T) {
	// Reset logger state
	ResetForTesting()

	// Run concurrent operations
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	// Start multiple goroutines trying to initialize and get logger
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			if id%2 == 0 {
				// Even IDs: try to initialize with options
				err := InitializeWithOptions(
					WithLevel("debug"),
					WithComponent(fmt.Sprintf("test%d", id)),
				)
				if err != nil {
					errors <- fmt.Errorf("InitializeWithOptions failed: %w", err)
				}
			} else {
				// Odd IDs: get logger and use it
				logger := GetLogger()
				if logger == nil {
					errors <- fmt.Errorf("GetLogger returned nil")
					return
				}
				logger.Debugf("Test message from goroutine %d", id)
			}
		}(i)
	}

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - all goroutines completed
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out - possible deadlock")
	case err := <-errors:
		t.Fatal(err)
	}
}

// TestLoggerRaceCondition tests for race conditions in logger initialization
func TestLoggerRaceCondition(t *testing.T) {
	// Run multiple iterations to increase chance of catching races
	for iteration := 0; iteration < 5; iteration++ {
		t.Run(fmt.Sprintf("iteration_%d", iteration), func(t *testing.T) {
			// Reset logger state
			ResetForTesting()

			const numGoroutines = 20
			var wg sync.WaitGroup
			start := make(chan struct{})

			// Create goroutines that will all start at the same time
			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()

					// Wait for start signal to maximize contention
					<-start

					// Mix of operations
					switch id % 3 {
					case 0:
						_ = InitializeWithOptions(WithLevel("info"))
					case 1:
						logger := GetLogger()
						if logger != nil {
							logger.Infof("Message from goroutine %d", id)
						}
					case 2:
						logger := GetLogger()
						if logger != nil && logger.IsInfoEnabled() {
							logger.Infof("Conditional message from goroutine %d", id)
						}
					}
				}(i)
			}

			// Start all goroutines at once
			close(start)

			// Wait for completion
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				// Success
			case <-time.After(2 * time.Second):
				t.Fatal("Race condition test timed out")
			}
		})
	}
}

// TestGetLoggerAlwaysReturnsNonNil verifies GetLogger never returns nil
func TestGetLoggerAlwaysReturnsNonNil(t *testing.T) {
	ResetForTesting()

	// Test multiple concurrent calls
	const numCalls = 100
	results := make(chan *Logger, numCalls)

	for i := 0; i < numCalls; i++ {
		go func() {
			results <- GetLogger()
		}()
	}

	// Collect all results
	for i := 0; i < numCalls; i++ {
		logger := <-results
		if logger == nil {
			t.Error("GetLogger returned nil")
		}
	}
}
