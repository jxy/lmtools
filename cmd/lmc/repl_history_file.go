package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
)

const (
	// replHistoryFileName lives in the sessions directory, so -sessions-dir
	// moves the history with the conversations it came from.
	replHistoryFileName = ".repl-history"
	// maxSavedHistoryLineBytes bounds one saved line, JSON escaping included,
	// so a pasted log or markup heavy in escaped characters stays in this
	// process's history only.
	maxSavedHistoryLineBytes = 64 * 1024
	// maxHistoryFileBytes is how much of the file's end is read. A file
	// larger than this, or holding more than twice the retained entries, is
	// rewritten to the entries kept.
	maxHistoryFileBytes = 4 * 1024 * 1024
)

// historyFile keeps REPL history across invocations, one JSON object per
// line. Every read and write holds an exclusive lock on the file, so two
// REPLs appending at once write whole lines, and a load that compacts the
// file reads and rewrites it in one hold. Failures are reported once and then
// ignored: history is a convenience, never a reason to stop a conversation.
type historyFile struct {
	path   string
	warn   func(format string, args ...interface{})
	failed bool
}

type historyRecord struct {
	Text    string `json:"text"`
	Command bool   `json:"command,omitempty"`
}

// loadHistoryFile reads the history saved under dir into history and makes
// later entries append to it.
func loadHistoryFile(history *replHistory, dir string, warn func(string, ...interface{})) {
	file := &historyFile{path: filepath.Join(dir, replHistoryFileName), warn: warn}
	err := file.locked(func(f *os.File) error {
		records, lines, size, err := readHistory(f)
		if err != nil {
			return err
		}
		for _, record := range records {
			history.remember(record.Text, record.Command)
		}
		if size > maxHistoryFileBytes || lines > 2*maxREPLHistoryEntries {
			return writeHistory(f, history.entries)
		}
		return nil
	})
	if err != nil {
		file.report("read REPL history %s: %v", file.path, err)
	}
	history.file = file
}

func (f *historyFile) report(format string, args ...interface{}) {
	if !f.failed && f.warn != nil {
		f.warn(format, args...)
	}
	f.failed = true
}

// readHistory reads the records of the file's last maxHistoryFileBytes. It
// returns how many lines that span held and the file's size, which decide a
// compaction. A line too long to be one this file writes is skipped, so one
// damaged line cannot hide the entries after it.
func readHistory(f *os.File) ([]historyRecord, int, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	size := info.Size()
	start := max(0, size-maxHistoryFileBytes)
	data := make([]byte, size-start)
	if _, err := f.ReadAt(data, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, 0, err
	}
	lines := bytes.Split(data, []byte("\n"))
	if start > 0 {
		// A span that starts mid-file starts mid-line.
		lines = lines[1:]
	}
	var records []historyRecord
	count := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		count++
		var record historyRecord
		if len(line) <= maxSavedHistoryLineBytes && json.Unmarshal(line, &record) == nil && record.Text != "" {
			records = append(records, record)
		}
	}
	return records, count, size, nil
}

// append adds one entry. An entry whose saved line would pass the bound is
// kept in memory only.
func (f *historyFile) append(text string, command bool) {
	// Encoding never shortens text, so a long prompt is not encoded at all.
	if len(text) > maxSavedHistoryLineBytes {
		return
	}
	line, err := json.Marshal(historyRecord{Text: text, Command: command})
	if err != nil || len(line) > maxSavedHistoryLineBytes {
		return
	}
	err = f.locked(func(file *os.File) error {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		_, err := file.Write(append(line, '\n'))
		return err
	})
	if err != nil {
		f.report("save REPL history to %s: %v", f.path, err)
	}
}

// clear empties the file.
func (f *historyFile) clear() error {
	return f.locked(func(file *os.File) error { return file.Truncate(0) })
}

// writeHistory replaces the file's contents with entries.
func writeHistory(file *os.File, entries []replPrompt) error {
	var buf bytes.Buffer
	for _, entry := range entries {
		line, err := json.Marshal(historyRecord{Text: entry.text, Command: entry.command})
		if err != nil {
			return err
		}
		if len(line) > maxSavedHistoryLineBytes {
			continue
		}
		buf.Write(append(line, '\n'))
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := file.Write(buf.Bytes())
	return err
}

// locked opens the file, created private to the user along with its
// directory, and runs use while holding an exclusive lock on it. The sessions
// directory may not exist yet: it is created by the first session, which
// commits after the first prompt has been added to history.
func (f *historyFile) locked(use func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(f.path), constants.DirPerm); err != nil {
		return err
	}
	file, err := os.OpenFile(f.path, os.O_RDWR|os.O_CREATE, constants.FilePerm)
	if err != nil {
		return err
	}
	defer file.Close()
	unlock, err := lockHistory(file)
	if err != nil {
		return err
	}
	defer unlock()
	return use(file)
}
