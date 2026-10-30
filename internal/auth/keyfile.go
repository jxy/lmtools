package auth

import (
	"os"
	"sync"
)

// KeyFile is a provider key read from a file that is read again whenever the
// file changes, so a long running process picks up a renewed token. A file
// replaced by rename, as alcfauth writes it, counts as changed even when its
// size and time match.
type KeyFile struct {
	provider string
	path     string
	warn     func(error)

	mu      sync.Mutex
	info    os.FileInfo
	key     ProviderKey
	failing bool
}

// OpenKeyFile reads the key in path for provider. warn, which may be nil,
// hears why a later read failed.
func OpenKeyFile(provider, path string, warn func(error)) (*KeyFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key, err := LoadProviderKeyFile(provider, path)
	if err != nil {
		return nil, err
	}
	return &KeyFile{provider: provider, path: path, warn: warn, info: info, key: key}, nil
}

// Key returns the key, reading the file again first when it changed since the
// last read. When the file cannot be read or holds no key, Key keeps the last
// key and warns once until a read succeeds.
func (k *KeyFile) Key() ProviderKey {
	k.mu.Lock()
	defer k.mu.Unlock()
	info, err := os.Stat(k.path)
	if err == nil && sameFileVersion(k.info, info) {
		return k.key
	}
	var key ProviderKey
	if err == nil {
		key, err = LoadProviderKeyFile(k.provider, k.path)
	}
	if err != nil {
		if !k.failing && k.warn != nil {
			k.warn(err)
		}
		k.failing = true
		return k.key
	}
	k.key, k.info, k.failing = key, info, false
	return k.key
}

func sameFileVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
