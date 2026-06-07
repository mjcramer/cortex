package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewStoreEmptyDirIsNoop(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, ok := s.(NoopStore); !ok {
		t.Fatalf("NewStore(\"\") = %T, want NoopStore", s)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	want := AgentState{
		Name:      "demo",
		ChannelID: "C123",
		History:   []Turn{{Role: "user", Text: "hi"}, {Role: "assistant", Text: "hello"}},
	}
	if err := s.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File is 0600.
	info, err := os.Stat(filepath.Join(dir, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	got, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LoadAll returned %d, want 1", len(got))
	}
	if got[0].Name != want.Name || got[0].ChannelID != want.ChannelID || len(got[0].History) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got[0])
	}

	if err := s.Delete(ctx, "demo"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, "demo"); err != nil {
		t.Fatalf("Delete (missing) should be a no-op, got: %v", err)
	}
	after, _ := s.LoadAll(ctx)
	if len(after) != 0 {
		t.Fatalf("after delete LoadAll returned %d, want 0", len(after))
	}
}

func TestFileStoreLoadAllIgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected non-JSON files ignored, got %d", len(got))
	}
}
