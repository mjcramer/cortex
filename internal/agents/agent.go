package agents

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// IncomingMessage is a single human-authored message delivered to an agent.
type IncomingMessage struct {
	UserID string
	Text   string
}

// Replier is whatever the agent uses to post messages back into Slack.
// The slack.App satisfies this; the test fake provides a stand-in.
type Replier interface {
	PostToChannel(ctx context.Context, channelID, text string) error
}

// Thinker turns a message (plus prior conversation) into the agent's response.
// The agent name is passed so a single Thinker instance can serve many agents
// with per-agent personalization (system prompts, etc.).
type Thinker interface {
	Respond(ctx context.Context, agentName string, history []Turn, incoming IncomingMessage) (string, error)
}

// Turn is a single message in the agent's conversation history.
type Turn struct {
	Role string // "user" or "assistant"
	Text string
}

type Agent struct {
	Name      string
	ChannelID string

	inbox   chan IncomingMessage
	replier Replier
	thinker Thinker
	logger  *slog.Logger

	mu      sync.Mutex
	history []Turn

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newAgent(parent context.Context, name, channelID string, replier Replier, thinker Thinker, logger *slog.Logger) *Agent {
	ctx, cancel := context.WithCancel(parent)
	return &Agent{
		Name:      name,
		ChannelID: channelID,
		inbox:     make(chan IncomingMessage, 32),
		replier:   replier,
		thinker:   thinker,
		logger:    logger.With("agent", name, "channel", channelID),
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
}

func (a *Agent) start() {
	go a.run()
}

func (a *Agent) stop() {
	a.cancel()
	<-a.done
}

func (a *Agent) deliver(msg IncomingMessage) {
	select {
	case a.inbox <- msg:
	default:
		a.logger.Warn("inbox full, dropping message", "user", msg.UserID)
	}
}

func (a *Agent) run() {
	defer close(a.done)
	a.logger.Info("agent started")
	defer a.logger.Info("agent stopped")

	for {
		select {
		case <-a.ctx.Done():
			return
		case msg := <-a.inbox:
			a.handle(msg)
		}
	}
}

func (a *Agent) handle(msg IncomingMessage) {
	a.mu.Lock()
	historySnapshot := append([]Turn(nil), a.history...)
	a.mu.Unlock()

	respCtx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()

	reply, err := a.thinker.Respond(respCtx, a.Name, historySnapshot, msg)
	if err != nil {
		a.logger.Error("thinker failed", "err", err)
		_ = a.replier.PostToChannel(respCtx, a.ChannelID, fmt.Sprintf("_(agent error: %v)_", err))
		return
	}

	a.mu.Lock()
	a.history = append(a.history, Turn{Role: "user", Text: msg.Text}, Turn{Role: "assistant", Text: reply})
	// Bound the history to keep prompts manageable.
	const maxTurns = 50
	if len(a.history) > maxTurns {
		a.history = append([]Turn(nil), a.history[len(a.history)-maxTurns:]...)
	}
	a.mu.Unlock()

	if err := a.replier.PostToChannel(respCtx, a.ChannelID, reply); err != nil {
		a.logger.Error("post reply failed", "err", err)
	}
}

// EchoThinker is the phase-1 placeholder. It echoes the user's message back
// with a small wrapper so it's obvious the round-trip worked.
type EchoThinker struct{}

func (EchoThinker) Respond(_ context.Context, _ string, _ []Turn, msg IncomingMessage) (string, error) {
	return fmt.Sprintf("(echo) %s", msg.Text), nil
}
