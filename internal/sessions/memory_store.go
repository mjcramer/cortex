package sessions

import (
	"context"
	"sync"
	"time"

	pb "github.com/mjcramer/cortex/internal/cortexpb"
)

type InMemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*session
	threads  map[ThreadRef]string
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		sessions: make(map[string]*session),
		threads:  make(map[ThreadRef]string),
	}
}

func (m *InMemoryStore) Register(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[sessionID]; ok {
		return ErrAlreadyExists
	}

	m.sessions[sessionID] = &session{done: make(chan struct{})}
	return nil
}

func (m *InMemoryStore) Remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return
	}
	delete(m.sessions, sessionID)

	s.mu.Lock()
	thread := s.slackThread
	s.mu.Unlock()

	if thread != nil {
		delete(m.threads, *thread)
	}
}

func (m *InMemoryStore) AttachSlackThread(sessionID string, thread ThreadRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slackThread != nil {
		return ErrAlreadyAttached
	}
	s.slackThread = &thread
	m.threads[thread] = sessionID
	return nil
}

func (m *InMemoryStore) FindBySlackThread(thread ThreadRef) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.threads[thread]
	return id, ok
}

func (m *InMemoryStore) Submit(reply *pb.HumanReply) error {
	m.mu.RLock()
	s, ok := m.sessions[reply.SessionId]
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	s.mu.Lock()
	if s.reply != nil {
		s.mu.Unlock()
		return ErrAlreadyResponded
	}
	s.reply = &storedReply{
		response:  reply.Response,
		responder: reply.Responder,
		source:    reply.Source,
	}
	done := s.done
	s.mu.Unlock()

	close(done)
	return nil
}

func (m *InMemoryStore) WaitForResponse(ctx context.Context, sessionID string, timeout time.Duration) *pb.HumanResponse {
	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return &pb.HumanResponse{
			SessionId: sessionID,
			Status:    pb.SessionStatus_NOT_FOUND,
		}
	}

	s.mu.Lock()
	if s.reply != nil {
		reply := *s.reply
		s.mu.Unlock()
		return respondedResponse(sessionID, reply)
	}
	done := s.done
	s.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		s.mu.Lock()
		reply := *s.reply
		s.mu.Unlock()
		return respondedResponse(sessionID, reply)
	case <-timer.C:
		return &pb.HumanResponse{
			SessionId: sessionID,
			Status:    pb.SessionStatus_TIMED_OUT,
		}
	case <-ctx.Done():
		return &pb.HumanResponse{
			SessionId: sessionID,
			Status:    pb.SessionStatus_TIMED_OUT,
		}
	}
}
