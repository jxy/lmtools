package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
	"testing"
)

func newTestCoordinatorConfig() core.RequestOptions {
	return core.NewTestRequestConfig()
}

func setupCoordinatorTestEnv(t *testing.T) context.Context {
	t.Helper()

	tempDir := t.TempDir()
	SetSessionsDir(tempDir)
	SetSkipFlockCheck(true)
	t.Cleanup(func() {
		SetSessionsDir("")
		SetSkipFlockCheck(false)
	})

	if err := logger.InitializeWithOptions(
		logger.WithLogDir(tempDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	return context.Background()
}

func stringPtr(s string) *string {
	return &s
}

func infoMessagesContain(notifier *core.TestNotifier, substr string) bool {
	for _, msg := range notifier.InfoMessages {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}
