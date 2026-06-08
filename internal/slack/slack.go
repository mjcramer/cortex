package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/mjcramer/cortex/internal/config"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
)

type Notifier interface {
	Notify(ctx context.Context, signal *pb.AgentSignal) (sessions.ThreadRef, error)
}

type App struct {
	cfg    *config.SlackConfig
	client *slack.Client
	log    *slog.Logger

	mu       sync.RWMutex
	channels map[string]string // agent name -> channel id
}

func NewApp(cfg *config.SlackConfig, logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	opts := []slack.Option{}
	if cfg.APIBaseURL != "" && cfg.APIBaseURL != "https://slack.com/api" {
		opts = append(opts, slack.OptionAPIURL(cfg.APIBaseURL+"/"))
	}
	return &App{
		cfg:      cfg,
		client:   slack.New(cfg.BotToken, opts...),
		log:      logger.With("component", "slack"),
		channels: make(map[string]string),
	}
}

func (a *App) ChannelNameFor(agent string) string {
	return a.cfg.ChannelPrefix + sanitizeChannelName(agent)
}

// VerifyAuth calls Slack's auth.test to confirm the bot token is valid. We
// run this at startup so an invalid or revoked token fails the boot rather
// than the first slash command.
func (a *App) VerifyAuth(ctx context.Context) (team, user string, err error) {
	resp, err := a.client.AuthTestContext(ctx)
	if err != nil {
		return "", "", fmt.Errorf("slack auth.test: %w", err)
	}
	return resp.Team, resp.User, nil
}

// EnsureChannelForAgent satisfies the agents.ChannelProvisioner interface.
func (a *App) EnsureChannelForAgent(ctx context.Context, agent string) (string, error) {
	return a.ensureChannel(ctx, agent)
}

// ArchiveChannel retires an agent's channel: it renames the channel aside to
// free its name, then archives it, then drops it from the per-agent cache.
//
// Why rename before archiving? A bot token cannot reliably reopen an archived
// channel later — `conversations.unarchive` returns not_in_channel once
// archiving drops the bot's membership, and you can't rejoin an archived
// channel. So rather than depend on reopening, we free the name now (while the
// channel is active and the bot is a member, so rename is allowed). A future
// agent with the same name then gets a clean new channel. Requires the
// channels:manage scope (already in the bot's scope set).
func (a *App) ArchiveChannel(ctx context.Context, channelID string) error {
	// Best-effort rename to free the name. If it fails (e.g. already archived),
	// proceed to archive anyway; the worst case is the name stays taken and a
	// later recreate surfaces a clear error from adoptExistingChannel.
	_, _ = a.client.RenameConversationContext(ctx, channelID, archivedChannelName(channelID))

	if err := a.client.ArchiveConversationContext(ctx, channelID); err != nil && !isSlackError(err, "already_archived") {
		return fmt.Errorf("archive channel %s: %w", channelID, err)
	}
	a.forgetChannelByID(channelID)
	return nil
}

// archivedChannelName derives a unique, valid channel name to park a retired
// channel under, freeing the original agent name. Channel IDs are unique and
// use only characters legal in channel names once lowercased.
func archivedChannelName(channelID string) string {
	return "archived-" + strings.ToLower(channelID)
}

// PostToChannel sends a plain message to the channel as the bot.
func (a *App) PostToChannel(ctx context.Context, channelID, text string) error {
	a.log.Debug("slack post → channel", "channel", channelID, "text", text)
	_, _, err := a.client.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(text, false),
		slack.MsgOptionDisableLinkUnfurl(),
		slack.MsgOptionDisableMediaUnfurl(),
	)
	if err != nil {
		a.log.Error("slack post failed", "channel", channelID, "err", err)
		return fmt.Errorf("post to channel %s: %w", channelID, err)
	}
	a.log.Debug("slack post ok", "channel", channelID)
	return nil
}

func (a *App) Notify(ctx context.Context, signal *pb.AgentSignal) (sessions.ThreadRef, error) {
	channelID, err := a.ensureChannel(ctx, signal.Agent)
	if err != nil {
		return sessions.ThreadRef{}, err
	}

	a.log.Debug("slack post → agent signal",
		"agent", signal.Agent, "session_id", signal.SessionId,
		"channel", channelID, "text", signal.Message)

	postedChannel, ts, err := a.client.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(formatSignalMessage(signal), false),
		slack.MsgOptionUsername(signal.Agent),
		slack.MsgOptionDisableLinkUnfurl(),
		slack.MsgOptionDisableMediaUnfurl(),
	)
	if err != nil {
		a.log.Error("slack post (agent signal) failed",
			"agent", signal.Agent, "session_id", signal.SessionId, "err", err)
		return sessions.ThreadRef{}, fmt.Errorf("post slack message: %w", err)
	}
	if postedChannel == "" || ts == "" {
		return sessions.ThreadRef{}, errors.New("slack chat.postMessage response missing channel or ts")
	}

	a.log.Debug("slack post (agent signal) ok",
		"agent", signal.Agent, "session_id", signal.SessionId,
		"channel", postedChannel, "thread_ts", ts)
	return sessions.ThreadRef{ChannelID: postedChannel, ThreadTS: ts}, nil
}

func (a *App) VerifyRequest(headers http.Header, body []byte) error {
	verifier, err := slack.NewSecretsVerifier(headers, a.cfg.SigningSecret)
	if err != nil {
		return fmt.Errorf("init slack verifier: %w", err)
	}
	if _, err := verifier.Write(body); err != nil {
		return fmt.Errorf("write body to verifier: %w", err)
	}
	if err := verifier.Ensure(); err != nil {
		return fmt.Errorf("invalid slack request signature: %w", err)
	}
	return nil
}

func (a *App) ensureChannel(ctx context.Context, agent string) (string, error) {
	a.mu.RLock()
	if id, ok := a.channels[agent]; ok {
		a.mu.RUnlock()
		return id, nil
	}
	a.mu.RUnlock()

	name := a.ChannelNameFor(agent)
	a.log.Debug("slack ensure channel", "agent", agent, "channel", name)
	channel, err := a.client.CreateConversationContext(ctx, slack.CreateConversationParams{
		ChannelName: name,
		IsPrivate:   false,
	})
	switch {
	case err == nil:
		a.cacheChannel(agent, channel.ID)
		a.log.Debug("slack created channel", "agent", agent, "channel", name, "channel_id", channel.ID)
		return channel.ID, nil
	case isSlackError(err, "name_taken"):
		return a.adoptExistingChannel(ctx, agent, name)
	default:
		a.log.Error("slack create channel failed", "agent", agent, "channel", name, "err", err)
		return "", fmt.Errorf("create channel %s: %w", name, err)
	}
}

// adoptExistingChannel handles a name_taken on create: a channel with this name
// already exists. Normally that's an active channel the bot can simply join
// (e.g. created out of band). If it's archived, the bot generally cannot reopen
// it (see ArchiveChannel) — we make a best-effort unarchive but, failing that,
// return an actionable error rather than leaving the agent pointed at a dead
// channel. (Going forward, destroy frees the name via rename, so an archived
// channel under a live agent name should be a legacy/out-of-band case.)
func (a *App) adoptExistingChannel(ctx context.Context, agent, name string) (string, error) {
	ch, err := a.findChannel(ctx, name)
	if err != nil {
		return "", err
	}
	if ch == nil {
		return "", fmt.Errorf("slack reports %q is taken but it was not listable", name)
	}
	if ch.IsArchived {
		if err := a.client.UnArchiveConversationContext(ctx, ch.ID); err != nil {
			return "", fmt.Errorf("channel %q (%s) is archived and Cortex cannot reopen it with a bot token (%w); unarchive or rename it in Slack, then retry", name, ch.ID, err)
		}
	}
	if _, _, _, err := a.client.JoinConversationContext(ctx, ch.ID); err != nil && !isSlackError(err, "already_in_channel") {
		return "", fmt.Errorf("join existing channel %s: %w", ch.ID, err)
	}
	a.cacheChannel(agent, ch.ID)
	return ch.ID, nil
}

func (a *App) cacheChannel(agent, id string) {
	a.mu.Lock()
	a.channels[agent] = id
	a.mu.Unlock()
}

// forgetChannelByID removes any cache entry pointing at channelID, so the next
// ensureChannel re-resolves it (and can unarchive) instead of returning a dead
// channel. Called on archive.
func (a *App) forgetChannelByID(channelID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for agent, id := range a.channels {
		if id == channelID {
			delete(a.channels, agent)
		}
	}
}

// findChannel returns the channel with the given name, or nil if none exists.
// Archived channels are included so a destroyed agent's channel can be found
// and reopened on recreate.
func (a *App) findChannel(ctx context.Context, name string) (*slack.Channel, error) {
	params := &slack.GetConversationsParameters{
		ExcludeArchived: false,
		Limit:           200,
		Types:           []string{"public_channel"},
	}
	for {
		channels, nextCursor, err := a.client.GetConversationsContext(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		for i := range channels {
			if channels[i].Name == name {
				return &channels[i], nil
			}
		}
		if nextCursor == "" {
			return nil, nil
		}
		params.Cursor = nextCursor
	}
}

// isSlackError reports whether err carries the given Slack API error code.
// slack-go surfaces these in a few different shapes (SlackErrorResponse for
// API ok:false, plain errors.New for some call sites), so we type-check the
// structured case and fall back to the error string.
func isSlackError(err error, code string) bool {
	if err == nil {
		return false
	}
	var apiErr slack.SlackErrorResponse
	if errors.As(err, &apiErr) {
		return apiErr.Err == code
	}
	return err.Error() == code || strings.Contains(err.Error(), code)
}

type ThreadReply struct {
	Thread sessions.ThreadRef
	UserID string
	Text   string
}

// ParseEvent decodes a Slack Events API envelope. Signature verification must
// be performed by the caller (we already do this in App.VerifyRequest before
// reaching here).
func ParseEvent(body []byte) (slackevents.EventsAPIEvent, error) {
	return slackevents.ParseEvent(body, slackevents.OptionNoVerifyToken())
}

// HumanThreadReply extracts a thread reply from a Slack message event, ignoring
// bot messages, message subtypes (edits/deletes), and top-level (non-threaded)
// messages.
func HumanThreadReply(event *slackevents.MessageEvent) *ThreadReply {
	if event == nil || event.Type != "message" {
		return nil
	}
	if event.SubType != "" || event.BotID != "" {
		return nil
	}
	if event.Channel == "" || event.ThreadTimeStamp == "" || event.User == "" {
		return nil
	}
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return nil
	}
	return &ThreadReply{
		Thread: sessions.ThreadRef{ChannelID: event.Channel, ThreadTS: event.ThreadTimeStamp},
		UserID: event.User,
		Text:   text,
	}
}

func formatSignalMessage(signal *pb.AgentSignal) string {
	repo := signal.Repo
	if repo == "" {
		repo = "unknown repo"
	}
	return fmt.Sprintf(
		"*%s* needs a human response.\nRepo: `%s`\nSession: `%s`\n\n%s\n\nReply in this thread as you would to any teammate message.",
		signal.Agent, repo, signal.SessionId, signal.Message,
	)
}

func sanitizeChannelName(agent string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(agent) {
		var mapped rune
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_':
			mapped = r
		case r == '-' || r == ' ' || r == '.' || r == '/' || r == ':':
			mapped = '-'
		default:
			continue
		}
		if mapped == '-' {
			if lastDash || b.Len() == 0 {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		b.WriteRune(mapped)
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "unnamed"
	}
	if len(out) > 60 {
		out = strings.TrimRight(out[:60], "-")
	}
	return out
}
