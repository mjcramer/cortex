package sessions

import (
	"context"
	"errors"
	"sync"
	"time"

	pb "github.com/mjcramer/cortex/internal/cortexpb"
)

var (
	ErrAlreadyExists    = errors.New("session already exists")
	ErrNotFound         = errors.New("session not found")
	ErrAlreadyResponded = errors.New("session already has a response")
	ErrAlreadyAttached  = errors.New("session already has a slack thread attached")
)

type ThreadRef struct {
	ChannelID string
	ThreadTS  string
}

type storedReply struct {
	response  string
	responder string
	source    string
}

// NewStore returns an in-memory store when dir is empty, or a file-backed store
// rooted at dir when persistence is enabled.
func NewStore(dir string) (SessionStore, error) {
	if dir == "" {
		return NewInMemoryStore(), nil
	}
	return NewFileStore(dir)
}

type SessionStore interface {
	Register(sessionID string) error
	Remove(sessionID string)
	AttachSlackThread(sessionID string, thread ThreadRef) error
	FindBySlackThread(thread ThreadRef) (string, bool)
	Submit(reply *pb.HumanReply) error
	WaitForResponse(ctx context.Context, sessionID string, timeout time.Duration) *pb.HumanResponse
}

type session struct {
	mu          sync.Mutex
	reply       *storedReply
	slackThread *ThreadRef
	done        chan struct{}
}

type Manager struct {
	store SessionStore
}

func NewManager() *Manager {
	return NewManagerWithStore(NewInMemoryStore())
}

func NewManagerWithStore(store SessionStore) *Manager {
	return &Manager{store: store}
}

func (m *Manager) Register(sessionID string) error {
	return m.store.Register(sessionID)
}

func (m *Manager) Remove(sessionID string) {
	m.store.Remove(sessionID)
}

func (m *Manager) AttachSlackThread(sessionID string, thread ThreadRef) error {
	return m.store.AttachSlackThread(sessionID, thread)
}

func (m *Manager) FindBySlackThread(thread ThreadRef) (string, bool) {
	return m.store.FindBySlackThread(thread)
}

func (m *Manager) Submit(reply *pb.HumanReply) error {
	return m.store.Submit(reply)
}

func (m *Manager) WaitForResponse(ctx context.Context, sessionID string, timeout time.Duration) *pb.HumanResponse {
	return m.store.WaitForResponse(ctx, sessionID, timeout)
}

func respondedResponse(sessionID string, reply storedReply) *pb.HumanResponse {
	return &pb.HumanResponse{
		SessionId: sessionID,
		Response:  reply.response,
		Responder: reply.responder,
		Source:    reply.source,
		Status:    pb.SessionStatus_RESPONDED,
	}
}
