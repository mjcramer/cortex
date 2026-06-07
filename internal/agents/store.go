package agents

import (
	"context"
	"sync"
)

// AgentState is the persistable snapshot of an agent: enough to recreate it
// (channel + conversation) after a restart without re-provisioning the channel.
type AgentState struct {
	Name      string `json:"name"`
	ChannelID string `json:"channel_id"`
	History   []Turn `json:"history,omitempty"`
}

// Store persists agent state so agents survive restarts. Implementations must
// be safe for concurrent use — each running agent saves its own state after
// every turn.
type Store interface {
	Save(ctx context.Context, state AgentState) error
	Delete(ctx context.Context, name string) error
	LoadAll(ctx context.Context) ([]AgentState, error)
}

// NewStore returns a FileStore rooted at dir, or a NoopStore when dir is empty
// (persistence disabled — the default).
func NewStore(dir string) (Store, error) {
	if dir == "" {
		return NoopStore{}, nil
	}
	return NewFileStore(dir)
}

// NoopStore disables persistence: nothing is written and LoadAll is empty. It
// is the default so the in-memory runtime behaves exactly as before. Note this
// is NOT an in-memory store — agent state still lives in the Manager's maps;
// NoopStore simply means that state is never mirrored to durable storage.
type NoopStore struct{}

func (NoopStore) Save(context.Context, AgentState) error        { return nil }
func (NoopStore) Delete(context.Context, string) error          { return nil }
func (NoopStore) LoadAll(context.Context) ([]AgentState, error) { return nil, nil }

// MemoryStore keeps state in a map. It does NOT survive restarts (it's process
// memory); it exists for tests that assert save/delete/load behavior without
// touching disk.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]AgentState
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: make(map[string]AgentState)}
}

func (s *MemoryStore) Save(_ context.Context, st AgentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[st.Name] = st
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}

func (s *MemoryStore) LoadAll(_ context.Context) ([]AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentState, 0, len(s.m))
	for _, st := range s.m {
		out = append(out, st)
	}
	return out, nil
}
