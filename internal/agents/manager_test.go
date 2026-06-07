package agents

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeProvisioner struct {
	mu       sync.Mutex
	ensured  int
	archived []string
}

func (f *fakeProvisioner) EnsureChannelForAgent(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured++
	return "C-" + name, nil
}

func (f *fakeProvisioner) ArchiveChannel(_ context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived = append(f.archived, channelID)
	return nil
}

type fakeReplier struct{ posts chan string }

func (f *fakeReplier) PostToChannel(_ context.Context, _, text string) error {
	f.posts <- text
	return nil
}

type fakeThinker struct {
	reply string

	mu          sync.Mutex
	lastHistory []Turn
}

func (f *fakeThinker) Respond(_ context.Context, _ string, history []Turn, _ IncomingMessage) (string, error) {
	f.mu.Lock()
	f.lastHistory = append([]Turn(nil), history...)
	f.mu.Unlock()
	return f.reply, nil
}

func (f *fakeThinker) history() []Turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastHistory
}

func newTestManager(store Store) (*Manager, *fakeProvisioner, *fakeReplier, *fakeThinker) {
	prov := &fakeProvisioner{}
	replier := &fakeReplier{posts: make(chan string, 8)}
	thinker := &fakeThinker{reply: "pong"}
	return NewManager(prov, replier, thinker, store, testLogger()), prov, replier, thinker
}

func waitForPost(t *testing.T, replier *fakeReplier) string {
	t.Helper()
	select {
	case p := <-replier.posts:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a posted reply")
		return ""
	}
}

func TestManagerCreatePersistsThenTurnUpdatesHistory(t *testing.T) {
	store := NewMemoryStore()
	mgr, _, replier, _ := newTestManager(store)
	ctx := context.Background()

	ch, err := mgr.Create(ctx, "demo")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ch != "C-demo" {
		t.Fatalf("channel = %q, want C-demo", ch)
	}

	// Initial state persisted before any turn.
	states, _ := store.LoadAll(ctx)
	if len(states) != 1 || states[0].Name != "demo" || states[0].ChannelID != "C-demo" {
		t.Fatalf("after create, store = %+v", states)
	}

	if !mgr.RouteMessage("C-demo", IncomingMessage{UserID: "U1", Text: "ping"}) {
		t.Fatal("RouteMessage returned false for the agent's channel")
	}
	if got := waitForPost(t, replier); got != "pong" {
		t.Fatalf("posted reply = %q, want pong", got)
	}

	// persist runs before the reply is posted, so by now history is saved.
	states, _ = store.LoadAll(ctx)
	if len(states) != 1 || len(states[0].History) != 2 {
		t.Fatalf("after turn, persisted history = %+v", states)
	}
}

func TestManagerDestroyDeletesState(t *testing.T) {
	store := NewMemoryStore()
	mgr, prov, _, _ := newTestManager(store)
	ctx := context.Background()

	if _, err := mgr.Create(ctx, "demo"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Destroy(ctx, "demo"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	states, _ := store.LoadAll(ctx)
	if len(states) != 0 {
		t.Fatalf("after destroy, store = %+v, want empty", states)
	}
	if len(prov.archived) != 1 || prov.archived[0] != "C-demo" {
		t.Fatalf("archived = %v, want [C-demo]", prov.archived)
	}
}

func TestManagerRestoreSpawnsAgentsWithHistory(t *testing.T) {
	store := NewMemoryStore()
	_ = store.Save(context.Background(), AgentState{
		Name:      "demo",
		ChannelID: "C-demo",
		History:   []Turn{{Role: "user", Text: "earlier"}, {Role: "assistant", Text: "reply"}},
	})

	mgr, prov, replier, thinker := newTestManager(store)
	if err := mgr.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Restore must not re-provision channels.
	if prov.ensured != 0 {
		t.Fatalf("provisioner called %d times during restore, want 0", prov.ensured)
	}
	if names := mgr.List(); len(names) != 1 || names[0] != "demo" {
		t.Fatalf("List = %v, want [demo]", names)
	}

	// A routed message reaches the restored agent, and the thinker sees the
	// restored history.
	if !mgr.RouteMessage("C-demo", IncomingMessage{UserID: "U1", Text: "again"}) {
		t.Fatal("RouteMessage returned false for restored agent")
	}
	waitForPost(t, replier)

	hist := thinker.history()
	if len(hist) != 2 || hist[0].Text != "earlier" {
		t.Fatalf("thinker saw history %+v, want the 2 restored turns", hist)
	}
}
