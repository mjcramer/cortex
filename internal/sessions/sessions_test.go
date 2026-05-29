package sessions_test

import (
	"context"
	"testing"
	"time"

	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
)

func TestReturnsNotFoundForUnknownSession(t *testing.T) {
	m := sessions.NewManager()
	got := m.WaitForResponse(context.Background(), "missing", 10*time.Millisecond)
	if got.Status != pb.SessionStatus_NOT_FOUND {
		t.Fatalf("expected NOT_FOUND, got %v", got.Status)
	}
}

func TestWaitsForResponse(t *testing.T) {
	m := sessions.NewManager()
	if err := m.Register("session-1"); err != nil {
		t.Fatalf("register: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		if err := m.Submit(&pb.HumanReply{
			SessionId: "session-1",
			Response:  "approved",
			Responder: "U123",
			Source:    "slack:C123:1.2",
		}); err != nil {
			t.Errorf("submit: %v", err)
		}
	}()

	resp := m.WaitForResponse(context.Background(), "session-1", time.Second)
	if resp.Status != pb.SessionStatus_RESPONDED {
		t.Fatalf("status = %v, want RESPONDED", resp.Status)
	}
	if resp.Response != "approved" {
		t.Fatalf("response = %q, want approved", resp.Response)
	}
	if resp.Responder != "U123" {
		t.Fatalf("responder = %q, want U123", resp.Responder)
	}
}

func TestRejectsDuplicateSessions(t *testing.T) {
	m := sessions.NewManager()
	if err := m.Register("session-1"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := m.Register("session-1"); err != sessions.ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestFindsSessionBySlackThread(t *testing.T) {
	m := sessions.NewManager()
	if err := m.Register("session-1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	thread := sessions.ThreadRef{ChannelID: "C123", ThreadTS: "1710000000.1234"}
	if err := m.AttachSlackThread("session-1", thread); err != nil {
		t.Fatalf("attach: %v", err)
	}
	id, ok := m.FindBySlackThread(thread)
	if !ok || id != "session-1" {
		t.Fatalf("FindBySlackThread = %q, %v; want session-1, true", id, ok)
	}
}

func TestTimesOut(t *testing.T) {
	m := sessions.NewManager()
	if err := m.Register("session-1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	resp := m.WaitForResponse(context.Background(), "session-1", 20*time.Millisecond)
	if resp.Status != pb.SessionStatus_TIMED_OUT {
		t.Fatalf("status = %v, want TIMED_OUT", resp.Status)
	}
}
