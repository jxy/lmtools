package session

import (
	"encoding/json"
	"errors"
	"lmtools/internal/constants"
	lmerrors "lmtools/internal/errors"
	"os"
	"path/filepath"
)

// tempPrefix starts the name of every file and directory the store is still
// writing. Readers skip such names, and deletion clears those left behind.
const tempPrefix = ".tmp-"

// writeFileAtomic replaces path with data in one rename, after syncing data,
// so a reader sees the old file or the whole new one.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPrefix)
	if err != nil {
		return lmerrors.WrapError("create temporary file", err)
	}
	tmpPath := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(constants.FilePerm); err != nil {
		return lmerrors.WrapError("set file mode", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return lmerrors.WrapError("write temporary file", err)
	}
	if err := tmp.Sync(); err != nil {
		return lmerrors.WrapError("sync temporary file", err)
	}
	if err := tmp.Close(); err != nil {
		return lmerrors.WrapError("close temporary file", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		done = true
		return lmerrors.WrapError("rename temporary file", err)
	}
	done = true
	return nil
}

// writeJSON writes v to path atomically.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return lmerrors.WrapError("encode "+filepath.Base(path), err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// readJSON reads path into v. It reports false, and no error, when the file
// does not exist.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, lmerrors.WrapError("decode "+filepath.Base(path), err)
	}
	return true, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
