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

	sessionsDir := UseTestSessionDir(t)
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(sessionsDir),
		logger.WithLevel("debug"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	return context.Background()
}

func prepareSessionForTest(ctx context.Context, coordinator *Coordinator, inputStr string, isRegeneration bool, approver core.Approver) (*Session, bool, error) {
	plan, err := coordinator.PrepareRequest(ctx, inputStr, isRegeneration, approver, PendingToolExecute)
	if err != nil {
		return nil, false, err
	}
	sess, err := plan.Commit(ctx)
	if err != nil {
		return nil, false, err
	}
	return sess, plan.HasPendingTools, nil
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
