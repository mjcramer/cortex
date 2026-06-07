package agents

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrAgentExists   = errors.New("agent already exists")
	ErrAgentNotFound = errors.New("agent not found")
)

// ChannelProvisioner is what the manager uses to create / archive Slack
// channels. The slack.App satisfies it; tests can substitute a fake.
type ChannelProvisioner interface {
	EnsureChannelForAgent(ctx context.Context, agent string) (string, error)
	ArchiveChannel(ctx context.Context, channelID string) error
}

type Manager struct {
	provisioner ChannelProvisioner
	replier     Replier
	thinker     Thinker
	store       Store
	logger      *slog.Logger

	mu     sync.RWMutex
	byName map[string]*Agent
	byChan map[string]*Agent
}

func NewManager(provisioner ChannelProvisioner, replier Replier, thinker Thinker, store Store, logger *slog.Logger) *Manager {
	if store == nil {
		store = NoopStore{}
	}
	return &Manager{
		provisioner: provisioner,
		replier:     replier,
		thinker:     thinker,
		store:       store,
		logger:      logger,
		byName:      make(map[string]*Agent),
		byChan:      make(map[string]*Agent),
	}
}

// persist mirrors an agent's state to the Store. It runs on its own short
// context (agents call it from their goroutine) and logs rather than fails on
// error — a persistence hiccup must not break the live conversation.
func (m *Manager) persist(st AgentState) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.Save(ctx, st); err != nil {
		m.logger.Error("persist agent state failed", "name", st.Name, "err", err)
	}
}

// Restore reloads persisted agents and respawns their goroutines. It does NOT
// re-provision Slack channels — it trusts the stored channel ID — and skips any
// agent already present. Call once at startup.
func (m *Manager) Restore(ctx context.Context) error {
	m.logger.Debug("loading agents from store")
	states, err := m.store.LoadAll(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	restored := 0
	for _, st := range states {
		if st.Name == "" || st.ChannelID == "" {
			m.logger.Warn("skipping malformed persisted agent", "name", st.Name)
			continue
		}
		if _, ok := m.byName[st.Name]; ok {
			m.logger.Debug("agent already present; skipping restore", "name", st.Name)
			continue
		}
		agent := newAgent(context.Background(), st.Name, st.ChannelID, st.History, m.replier, m.thinker, m.persist, m.logger)
		agent.start()
		m.byName[st.Name] = agent
		m.byChan[st.ChannelID] = agent
		restored++
		m.logger.Info("restored agent from store",
			"name", st.Name, "channel", st.ChannelID, "history_turns", len(st.History))
	}
	if restored > 0 {
		m.logger.Info("agent restore complete", "restored", restored)
	}
	return nil
}

// Create provisions the Slack channel for the agent and spawns its goroutine.
// Returns the resolved channel ID so the caller can include a clickable link
// in the slash command response.
func (m *Manager) Create(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	if _, ok := m.byName[name]; ok {
		channelID := m.byName[name].ChannelID
		m.mu.Unlock()
		return channelID, ErrAgentExists
	}
	m.mu.Unlock()

	channelID, err := m.provisioner.EnsureChannelForAgent(ctx, name)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	if existing, ok := m.byName[name]; ok {
		m.mu.Unlock()
		return existing.ChannelID, ErrAgentExists
	}

	agent := newAgent(context.Background(), name, channelID, nil, m.replier, m.thinker, m.persist, m.logger)
	agent.start()
	m.byName[name] = agent
	m.byChan[channelID] = agent
	m.mu.Unlock()

	// Persist the initial state so the agent is known across a restart even
	// before its first turn.
	m.persist(AgentState{Name: name, ChannelID: channelID})
	return channelID, nil
}

// Destroy stops the agent's goroutine and archives its Slack channel.
func (m *Manager) Destroy(ctx context.Context, name string) error {
	m.mu.Lock()
	agent, ok := m.byName[name]
	if !ok {
		m.mu.Unlock()
		return ErrAgentNotFound
	}
	delete(m.byName, name)
	delete(m.byChan, agent.ChannelID)
	m.mu.Unlock()

	agent.stop()

	if err := m.store.Delete(ctx, name); err != nil {
		m.logger.Error("delete agent state failed", "name", name, "err", err)
	}

	if err := m.provisioner.ArchiveChannel(ctx, agent.ChannelID); err != nil {
		return err
	}
	return nil
}

// List returns the names of currently active agents, in no particular order.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.byName))
	for name := range m.byName {
		out = append(out, name)
	}
	return out
}

// RouteMessage delivers a channel message to the agent that owns the channel,
// if any. Returns false if the channel isn't owned by any agent (in which case
// the caller should fall through to its previous routing, e.g. thread-based
// session replies).
func (m *Manager) RouteMessage(channelID string, msg IncomingMessage) bool {
	m.mu.RLock()
	agent, ok := m.byChan[channelID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	agent.deliver(msg)
	return true
}

// Shutdown stops every agent. Channels are NOT archived — Destroy is the only
// path that does that.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	agents := make([]*Agent, 0, len(m.byName))
	for _, a := range m.byName {
		agents = append(agents, a)
	}
	m.byName = make(map[string]*Agent)
	m.byChan = make(map[string]*Agent)
	m.mu.Unlock()

	for _, a := range agents {
		a.stop()
	}
}
