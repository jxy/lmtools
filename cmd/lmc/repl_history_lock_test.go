//go:build darwin || freebsd || linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A load that compacts reads and rewrites the file in one hold of the lock,
// so an entry another REPL appends while the load waits survives it.
func TestREPLHistoryLoadReadsAndCompactsInOneLock(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 3*maxREPLHistoryEntries)
	for i := range lines {
		lines[i] = fmt.Sprintf(`{"text":"p%d"}`, i)
	}
	writeHistoryLines(t, dir, lines...)
	other, err := os.OpenFile(filepath.Join(dir, replHistoryFileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	var h replHistory
	loaded := make(chan struct{})
	go func() {
		defer close(loaded)
		loadHistoryFile(&h, dir, nil)
	}()
	// Give the load time to reach the file before the other REPL writes.
	time.Sleep(100 * time.Millisecond)
	if _, err := other.WriteString(`{"text":"late"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	<-loaded

	if last := h.entries[len(h.entries)-1].text; last != "late" {
		t.Fatalf("newest loaded entry = %q, want late", last)
	}
	saved := historyFileLines(t, dir)
	if len(saved) != maxREPLHistoryEntries || saved[len(saved)-1] != `{"text":"late"}` {
		t.Fatalf("compacted file holds %d lines ending %q", len(saved), saved[len(saved)-1])
	}
}
