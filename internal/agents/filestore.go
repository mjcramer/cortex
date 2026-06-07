package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileStore persists each agent as a JSON file (<name>.json) under a directory,
// writing atomically (temp file + rename) with 0600 perms.
//
// It is suitable for local development and warm in-instance restarts. It does
// NOT help Cloud Run scale-to-zero: each new container gets a fresh ephemeral
// disk, so a network-backed Store (GCS/Firestore) is needed there. The Store
// interface makes that a drop-in replacement.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir %s: %w", dir, err)
	}
	return &FileStore{dir: dir}, nil
}

// path maps an agent name to its file. Agent names are validated upstream
// (letters/digits/-/_), so they are safe to use directly as filenames.
func (s *FileStore) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

func (s *FileStore) Save(_ context.Context, st AgentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(st.Name) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write agent state: %w", err)
	}
	if err := os.Rename(tmp, s.path(st.Name)); err != nil {
		return fmt.Errorf("commit agent state: %w", err)
	}
	return nil
}

func (s *FileStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete agent state: %w", err)
	}
	return nil
}

func (s *FileStore) LoadAll(_ context.Context) ([]AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read state dir %s: %w", s.dir, err)
	}
	var out []AgentState
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var st AgentState
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		out = append(out, st)
	}
	return out, nil
}
