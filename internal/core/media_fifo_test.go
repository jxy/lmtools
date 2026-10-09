//go:build unix

package core

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLoadImageFileRejectsPeerlessFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("create FIFO: %v", err)
	}
	result := make(chan error, 1)
	go func() { _, err := LoadImageFile(path, 0); result <- err }()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO error=%v", err)
		}
	case <-time.After(time.Second):
		// Release a regressed blocking open before failing the test.
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = writer.Close()
		}
		<-result
		t.Fatal("image open blocked on a FIFO without a writer")
	}
}
