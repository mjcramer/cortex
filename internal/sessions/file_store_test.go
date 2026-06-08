package sessions_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
)

func TestNewStoreEmptyDirIsInMemory(t *testing.T) {
	store, err := sessions.NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, ok := store.(*sessions.InMemoryStore); !ok {
		t.Fatalf("NewStore(\"\") = %T, want *InMemoryStore", store)
	}
}

func TestFileStoreRestoresPendingSessionThread(t *testing.T) {
	dir := t.TempDir()
	store, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	thread := sessions.ThreadRef{ChannelID: "C123", ThreadTS: "1710000000.1234"}
	if err := store.Register("session/with/slashes"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := store.AttachSlackThread("session/with/slashes", thread); err != nil {
		t.Fatalf("AttachSlackThread: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("persisted files = %d, want 1", len(files))
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	restored, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatalf("restore NewStore: %v", err)
	}
	sessionID, ok := restored.FindBySlackThread(thread)
	if !ok || sessionID != "session/with/slashes" {
		t.Fatalf("FindBySlackThread = %q, %v; want session/with/slashes, true", sessionID, ok)
	}

	resp := restored.WaitForResponse(context.Background(), "session/with/slashes", time.Millisecond)
	if resp.Status != pb.SessionStatus_TIMED_OUT {
		t.Fatalf("status = %v, want TIMED_OUT", resp.Status)
	}
}

func TestFileStoreRestoresSubmittedResponse(t *testing.T) {
	dir := t.TempDir()
	store, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.Register("session-1"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := store.Submit(&pb.HumanReply{
		SessionId: "session-1",
		Response:  "approved",
		Responder: "U123",
		Source:    "slack:C123:1.2",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	restored, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatalf("restore NewStore: %v", err)
	}
	resp := restored.WaitForResponse(context.Background(), "session-1", time.Second)
	if resp.Status != pb.SessionStatus_RESPONDED {
		t.Fatalf("status = %v, want RESPONDED", resp.Status)
	}
	if resp.Response != "approved" || resp.Responder != "U123" || resp.Source != "slack:C123:1.2" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestFileStoreRemoveDeletesPersistedSession(t *testing.T) {
	dir := t.TempDir()
	store, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Register("session-1"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	store.Remove("session-1")

	restored, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatalf("restore NewStore: %v", err)
	}
	resp := restored.WaitForResponse(context.Background(), "session-1", time.Millisecond)
	if resp.Status != pb.SessionStatus_NOT_FOUND {
		t.Fatalf("status = %v, want NOT_FOUND", resp.Status)
	}
}
