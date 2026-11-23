package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextcfg"
	"os"
	"path/filepath"
)

const bindingFile = ".lmc-context.json"

func readBinding(path string) (contextcfg.Binding, bool, error) {
	for {
		data, err := os.ReadFile(filepath.Join(path, bindingFile))
		switch {
		case err == nil:
			var binding contextcfg.Binding
			if err := json.Unmarshal(data, &binding); err != nil {
				return binding, false, err
			}
			if err := binding.Validate(); err != nil {
				return binding, false, err
			}
			return binding, true, nil
		case !errors.Is(err, os.ErrNotExist):
			return contextcfg.Binding{}, false, err
		}
		if ok, _, _ := IsSiblingDir(filepath.Base(path)); !ok {
			return contextcfg.Binding{}, false, nil
		}
		path = filepath.Dir(path)
	}
}

// ContextBinding reads an atomically published binding without starting a server or writing session files.
func (m *Manager) ContextBinding(ref string) (contextcfg.Binding, bool, error) {
	if ref == "" {
		return contextcfg.Binding{}, false, nil
	}
	path := m.ResolveSessionPath(ref)
	if IsMessageReference(ref) {
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			return readBinding(path)
		}
		path = filepath.Dir(path)
	}
	return readBinding(path)
}

func writeBinding(path string, b contextcfg.Binding) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(path, bindingFile), raw)
}

// carryBindingLocked gives dir, a branch just made for a write or request
// that started in source, the binding source reads when dir would read
// another. A branch made from a message inside another branch is placed
// beside the message that branch replaces, where it would otherwise read
// the binding of the session it starts from. The caller holds the tree's
// lock.
func carryBindingLocked(source, dir string) error {
	want, found, err := readBinding(source)
	if err != nil || !found {
		return err
	}
	have, ok, err := readBinding(dir)
	if err != nil || (ok && have == want) {
		return err
	}
	return writeBinding(dir, want)
}

// BindContext publishes the first binding, under the same identity check as session reads.
func BindContext(ctx context.Context, s *Session, b contextcfg.Binding) error {
	if !b.Enabled() {
		return nil
	}
	return withTreeLock(ctx, s.Path, func() error {
		if s.Head != nil {
			if err := verifyHeadLocked(DefaultManager(), *s.Head); err != nil {
				return err
			}
		}
		existing, ok, err := readBinding(s.Path)
		if err != nil {
			return err
		}
		if ok && (existing.Notebook != "" || b.Notebook == "") {
			if existing.Notebook != b.Notebook {
				return fmt.Errorf("session notebook binding changed")
			}
			return nil
		}
		return writeBinding(s.Path, b)
	})
}
