package sessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "github.com/mjcramer/cortex/internal/cortexpb"
)

type sessionState struct {
	SessionID   string      `json:"session_id"`
	SlackThread *ThreadRef  `json:"slack_thread,omitempty"`
	Reply       *replyState `json:"reply,omitempty"`
}

type replyState struct {
	Response  string `json:"response"`
	Responder string `json:"responder"`
	Source    string `json:"source"`
}

// FileStore persists session state as JSON while using InMemoryStore for live
// wait channels. Session IDs are base64url-encoded into filenames so callers do
// not need to supply path-safe IDs.
type FileStore struct {
	dir string
	mu  sync.Mutex
	mem *InMemoryStore
}

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session state dir %s: %w", dir, err)
	}
	s := &FileStore{dir: dir, mem: NewInMemoryStore()}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStore) Register(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mem.Register(sessionID); err != nil {
		return err
	}
	if err := s.saveLocked(sessionID); err != nil {
		s.mem.Remove(sessionID)
		return err
	}
	return nil
}

func (s *FileStore) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.mem.Remove(sessionID)
	if err := os.Remove(s.path(sessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Keep the interface compatible with the original fire-and-forget remove.
		return
	}
}

func (s *FileStore) AttachSlackThread(sessionID string, thread ThreadRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mem.AttachSlackThread(sessionID, thread); err != nil {
		return err
	}
	if err := s.saveLocked(sessionID); err != nil {
		return err
	}
	return nil
}

func (s *FileStore) FindBySlackThread(thread ThreadRef) (string, bool) {
	return s.mem.FindBySlackThread(thread)
}

func (s *FileStore) Submit(reply *pb.HumanReply) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.mem.Submit(reply); err != nil {
		return err
	}
	if err := s.saveLocked(reply.SessionId); err != nil {
		return err
	}
	return nil
}

func (s *FileStore) WaitForResponse(ctx context.Context, sessionID string, timeout time.Duration) *pb.HumanResponse {
	return s.mem.WaitForResponse(ctx, sessionID, timeout)
}

func (s *FileStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read session state dir %s: %w", s.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var st sessionState
		if err := json.Unmarshal(data, &st); err != nil {
			return fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if st.SessionID == "" {
			return fmt.Errorf("parse %s: missing session_id", e.Name())
		}
		if err := s.restore(st); err != nil {
			return fmt.Errorf("restore %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *FileStore) restore(st sessionState) error {
	if err := s.mem.Register(st.SessionID); err != nil {
		return err
	}
	if st.SlackThread != nil {
		if err := s.mem.AttachSlackThread(st.SessionID, *st.SlackThread); err != nil {
			return err
		}
	}
	if st.Reply != nil {
		if err := s.mem.Submit(&pb.HumanReply{
			SessionId: st.SessionID,
			Response:  st.Reply.Response,
			Responder: st.Reply.Responder,
			Source:    st.Reply.Source,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileStore) saveLocked(sessionID string) error {
	st, ok := s.snapshot(sessionID)
	if !ok {
		return ErrNotFound
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(sessionID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	if err := os.Rename(tmp, s.path(sessionID)); err != nil {
		return fmt.Errorf("commit session state: %w", err)
	}
	return nil
}

func (s *FileStore) snapshot(sessionID string) (sessionState, bool) {
	s.mem.mu.RLock()
	sess, ok := s.mem.sessions[sessionID]
	s.mem.mu.RUnlock()
	if !ok {
		return sessionState{}, false
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	st := sessionState{SessionID: sessionID}
	if sess.slackThread != nil {
		thread := *sess.slackThread
		st.SlackThread = &thread
	}
	if sess.reply != nil {
		st.Reply = &replyState{
			Response:  sess.reply.response,
			Responder: sess.reply.responder,
			Source:    sess.reply.source,
		}
	}
	return st, true
}

func (s *FileStore) path(sessionID string) string {
	return filepath.Join(s.dir, encodeSessionID(sessionID)+".json")
}

func encodeSessionID(sessionID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(sessionID))
}
