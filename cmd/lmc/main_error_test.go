package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/ui"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportErrorOncePreservesCausesAndExitCode(t *testing.T) {
	pathErr := &os.PathError{Op: "write", Path: "session", Err: os.ErrPermission}
	for _, cause := range []error{pathErr, context.Canceled, core.ErrInterrupted} {
		var localOutput, outerOutput bytes.Buffer
		local, outer := ui.NewNotifierWithWriter(&localOutput), ui.NewNotifierWithWriter(&outerOutput)
		err := fmt.Errorf("operation: %w", cause)
		reported := reportError(local, err)
		returned := reportError(outer, fmt.Errorf("caller: %w", reported))
		if localOutput.String() != "Error: "+err.Error()+"\n" || outerOutput.Len() != 0 {
			t.Fatalf("local=%q outer=%q", localOutput.String(), outerOutput.String())
		}
		if !errors.Is(returned, cause) || getExitCode(returned) != getExitCode(err) {
			t.Fatalf("reporting changed cause or exit code: %v", returned)
		}
		if cause == pathErr {
			var got *os.PathError
			if !errors.As(returned, &got) || got != pathErr {
				t.Fatal("reporting lost the concrete filesystem error")
			}
		}
		if reportError(outer, nil) != nil || outerOutput.Len() != 0 {
			t.Fatal("successful exit printed an error")
		}
	}
}

func readProcessLog(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_lmc_*.log"))
	if err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(data)
	}
	return contents.String()
}

func TestSendRequestFailureLogging(t *testing.T) {
	for _, level := range []string{"INFO", "WARN", "ERROR"} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancelled=%v", level, cancelled), func(t *testing.T) {
				logger.ResetForTesting()
				t.Cleanup(logger.ResetForTesting)
				diagnostics, err := os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
				originalStderr := os.Stderr
				os.Stderr = diagnostics
				t.Cleanup(func() {
					os.Stderr = originalStderr
					_ = diagnostics.Close()
				})
				cfg := config.Config{LogLevel: level, LogDir: t.TempDir(), Timeout: time.Second}
				notifier := ui.NewNotifierWithWriter(diagnostics)
				if _, err := setupLogging(cfg, notifier); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.WithValue(context.Background(), logger.RequestIDKey{}, "failed-request"))
				defer cancel()
				url := "unsupported://example.test"
				if cancelled {
					cancel()
					url = "http://127.0.0.1:1"
				}
				req, err := http.NewRequest(http.MethodPost, url, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, requestErr := sendWithRetry(ctx, req, &cfg)
				if requestErr == nil {
					t.Fatal("request unexpectedly succeeded")
				}
				if !cancelled {
					_ = reportError(notifier, requestErr)
				}
				logged := readProcessLog(t, cfg.LogDir)
				visible, err := os.ReadFile(diagnostics.Name())
				if err != nil {
					t.Fatal(err)
				}
				if cancelled {
					if !errors.Is(requestErr, context.Canceled) || len(visible) != 0 || strings.Contains(logged, "[ERROR]") {
						t.Fatalf("cancellation=%v diagnostics=%q log=%q", requestErr, visible, logged)
					}
					if level == "INFO" && !strings.Contains(logged, "Request cancelled") {
						t.Fatalf("missing cancellation record: %q", logged)
					}
					return
				}
				if string(visible) != "Error: "+requestErr.Error()+"\n" {
					t.Errorf("diagnostic duplicated or changed: %q", visible)
				}
				if strings.Count(logged, "Request failed") != 1 || !strings.Contains(logged, "[ERROR]") ||
					!strings.Contains(logged, "[#failed-request]") || !strings.Contains(logged, requestErr.Error()) {
					t.Fatalf("failure missing its severity, identity, or cause: %q", logged)
				}
			})
		}
	}
}
