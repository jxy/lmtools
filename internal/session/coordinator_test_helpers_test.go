package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
	"testing"
)

type testCoordinatorConfig struct {
	*core.TestRequestConfig
	EffectiveSystemOverride *string
}

func newTestCoordinatorConfig() *testCoordinatorConfig {
	return &testCoordinatorConfig{
		TestRequestConfig: core.NewTestRequestConfig(),
	}
}

func (c *testCoordinatorConfig) GetEffectiveSystem() string {
	if c.EffectiveSystemOverride != nil {
		return *c.EffectiveSystemOverride
	}
	return c.TestRequestConfig.GetEffectiveSystem()
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
