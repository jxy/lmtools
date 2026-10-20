package logger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestLoggerFileErrorRouting(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			ResetForTesting()
			t.Cleanup(ResetForTesting)
			dir := t.TempDir()
			stderr, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stderr
			os.Stderr = stderr
			t.Cleanup(func() {
				os.Stderr = original
				_ = stderr.Close()
			})
			if err := InitializeWithOptions(WithLogDir(dir), WithLevel("ERROR"), WithFormat(format),
				WithFile(true), WithStderr(true), WithComponent("test")); err != nil {
				t.Fatal(err)
			}
			log := From(context.WithValue(context.Background(), RequestIDKey{}, "request-42"))
			log.FileErrorf("returned failure: %s", "details")
			// Suppressing one record's stderr must not affect later records.
			log.Errorf("background failure: %s", "details")
			visible, err := os.ReadFile(stderr.Name())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(visible), "returned failure") || strings.Count(string(visible), "background failure") != 1 {
				t.Fatalf("stderr routing=%q", visible)
			}
			files, err := filepath.Glob(filepath.Join(dir, "*.log"))
			if err != nil || len(files) != 1 {
				t.Fatalf("log files=%v err=%v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("log=%q", data)
			}
			for i, message := range []string{"returned failure: details", "background failure: details"} {
				if format == "json" {
					var record map[string]string
					if err := json.Unmarshal([]byte(lines[i]), &record); err != nil {
						t.Fatal(err)
					}
					if record["level"] != "ERROR" || record["request_id"] != "request-42" ||
						record["component"] != "test" || record["message"] != message {
						t.Errorf("record=%v", record)
					}
				} else if !strings.HasPrefix(lines[i], "[ERROR] ") ||
					!strings.HasSuffix(lines[i], "[test] [#request-42] "+message) {
					t.Errorf("record=%q", lines[i])
				}
			}
		})
	}
}
