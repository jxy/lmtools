package alcfauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
)

// cacheFile is the token cache in the layout alcf-tokens.sh writes:
//
//	{"app_name": ..., "client_id": ..., "by_resource_server": {"<id>": Token}}
//
// It is read and written through raw JSON so that fields and resource servers
// this package does not use survive a rewrite, and -cache can point at that
// tool's tokens-bash.json.
type cacheFile struct {
	fields  map[string]json.RawMessage
	servers map[string]json.RawMessage
}

func readCache(path string) (*cacheFile, error) {
	c := &cacheFile{fields: map[string]json.RawMessage{}, servers: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read token cache: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, &c.fields); err != nil {
		return nil, fmt.Errorf("parse token cache %s: %w", path, err)
	}
	if c.fields == nil {
		c.fields = map[string]json.RawMessage{}
	}
	if raw, ok := c.fields["by_resource_server"]; ok {
		if err := json.Unmarshal(raw, &c.servers); err != nil {
			return nil, fmt.Errorf("parse token cache %s: by_resource_server: %w", path, err)
		}
		if c.servers == nil {
			c.servers = map[string]json.RawMessage{}
		}
	}
	return c, nil
}

func (c *cacheFile) token(resourceServer string) (Token, bool, error) {
	raw, ok := c.servers[resourceServer]
	if !ok {
		return Token{}, false, nil
	}
	var tok Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return Token{}, false, fmt.Errorf("parse cached token for %s: %w", resourceServer, err)
	}
	return tok, true, nil
}

func (c *cacheFile) setToken(resourceServer string, tok Token) error {
	raw, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	c.servers[resourceServer] = raw
	return nil
}

func (c *cacheFile) deleteToken(resourceServer string) {
	delete(c.servers, resourceServer)
}

func (c *cacheFile) str(key string) string {
	var s string
	if raw, ok := c.fields[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func (c *cacheFile) setStr(key, value string) {
	raw, _ := json.Marshal(value)
	c.fields[key] = raw
}

func (c *cacheFile) write(path string) error {
	servers, err := json.Marshal(c.servers)
	if err != nil {
		return err
	}
	c.fields["by_resource_server"] = servers
	data, err := json.MarshalIndent(c.fields, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return fmt.Errorf("write token cache: %w", err)
	}
	return nil
}

// writeKeyFile puts the access token in the file lmc reads with -api-key-file.
// lmc reads that file for every request, so the rename matters: a reader sees
// the old token or the new one, never a partial write.
func writeKeyFile(path, accessToken string) error {
	if err := writeFileAtomic(path, []byte(accessToken+"\n")); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temporary file next to path, mode 0600, and
// renames it over path. Missing directories are created with mode 0700.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, constants.DirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(constants.FilePerm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	tmp = nil
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
