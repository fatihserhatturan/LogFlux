package sources

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Checkpoint records how far a file has been read
type Checkpoint struct {
	FileID string `json:"file_id,omitempty"` // device:inode, empty where unsupported
	Offset int64  `json:"offset"`
}

// Checkpoints is a goroutine-safe, file-backed map of path -> Checkpoint
type Checkpoints struct {
	path string

	mu sync.Mutex
	m  map[string]Checkpoint
}

// LoadCheckpoints reads checkpoints from path (missing file = empty)
func LoadCheckpoints(path string) (*Checkpoints, error) {
	c := &Checkpoints{path: path, m: make(map[string]Checkpoint)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoints: %w", err)
	}
	if err := json.Unmarshal(data, &c.m); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoints %s: %w", path, err)
	}
	return c, nil
}

// Get returns the checkpoint for a file path
func (c *Checkpoints) Get(path string) (Checkpoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp, ok := c.m[path]
	return cp, ok
}

// Set records the checkpoint for a file path
func (c *Checkpoints) Set(path string, cp Checkpoint) {
	c.mu.Lock()
	c.m[path] = cp
	c.mu.Unlock()
}

// Delete removes the checkpoint for a file path
func (c *Checkpoints) Delete(path string) {
	c.mu.Lock()
	delete(c.m, path)
	c.mu.Unlock()
}

// Save writes checkpoints atomically (temp file + rename)
func (c *Checkpoints) Save() error {
	c.mu.Lock()
	data, err := json.MarshalIndent(c.m, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
