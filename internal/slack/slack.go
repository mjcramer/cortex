package slack

import (
	"context"
	"errors"
	"fmt"
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

	mu       sync.RWMutex
	channels map[string]string // agent name -> channel id
}

func NewApp(cfg *config.SlackConfig) *App {
	opts := []slack.Option{}
	if cfg.APIBaseURL != "" && cfg.APIBaseURL != "https://slack.com/api" {
		opts = append(opts, slack.OptionAPIURL(cfg.APIBaseURL+"/"))
	}
	return &App{
		cfg:      cfg,
		client:   slack.New(cfg.BotToken, opts...),
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

// ArchiveChannel archives a public channel by ID. Requires the channels:manage
// scope (already in the bot's scope set).
func (a *App) ArchiveChannel(ctx context.Context, channelID string) error {
	if err := a.client.ArchiveConversationContext(ctx, channelID); err != nil {
		if isSlackError(err, "already_archived") {
			return nil
		}
		return fmt.Errorf("archive channel %s: %w", channelID, err)
	}
	return nil
}

// PostToChannel sends a plain message to the channel as the bot.
func (a *App) PostToChannel(ctx context.Context, channelID, text string) error {
	_, _, err := a.client.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(text, false),
		slack.MsgOptionDisableLinkUnfurl(),
		slack.MsgOptionDisableMediaUnfurl(),
	)
	if err != nil {
		return fmt.Errorf("post to channel %s: %w", channelID, err)
	}
	return nil
}

func (a *App) Notify(ctx context.Context, signal *pb.AgentSignal) (sessions.ThreadRef, error) {
	channelID, err := a.ensureChannel(ctx, signal.Agent)
	if err != nil {
		return sessions.ThreadRef{}, err
	}

	postedChannel, ts, err := a.client.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(formatSignalMessage(signal), false),
		slack.MsgOptionUsername(signal.Agent),
		slack.MsgOptionDisableLinkUnfurl(),
		slack.MsgOptionDisableMediaUnfurl(),
	)
	if err != nil {
		return sessions.ThreadRef{}, fmt.Errorf("post slack message: %w", err)
	}
	if postedChannel == "" || ts == "" {
		return sessions.ThreadRef{}, errors.New("slack chat.postMessage response missing channel or ts")
	}

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
	channel, err := a.client.CreateConversationContext(ctx, slack.CreateConversationParams{
		ChannelName: name,
		IsPrivate:   false,
	})
	switch {
	case err == nil:
		a.cacheChannel(agent, channel.ID)
		return channel.ID, nil
	case isSlackError(err, "name_taken"):
		id, lookupErr := a.findChannel(ctx, name)
		if lookupErr != nil {
			return "", lookupErr
		}
		if id == "" {
			return "", fmt.Errorf("slack reports %q is taken but it was not listable", name)
		}
		if _, _, _, joinErr := a.client.JoinConversationContext(ctx, id); joinErr != nil && !isSlackError(joinErr, "already_in_channel") {
			return "", fmt.Errorf("join existing channel %s: %w", id, joinErr)
		}
		a.cacheChannel(agent, id)
		return id, nil
	default:
		return "", fmt.Errorf("create channel %s: %w", name, err)
	}
}

func (a *App) cacheChannel(agent, id string) {
	a.mu.Lock()
	a.channels[agent] = id
	a.mu.Unlock()
}

func (a *App) findChannel(ctx context.Context, name string) (string, error) {
	params := &slack.GetConversationsParameters{
		ExcludeArchived: true,
		Limit:           200,
		Types:           []string{"public_channel"},
	}
	for {
		channels, nextCursor, err := a.client.GetConversationsContext(ctx, params)
		if err != nil {
			return "", fmt.Errorf("list channels: %w", err)
		}
		for _, ch := range channels {
			if ch.Name == name {
				return ch.ID, nil
			}
		}
		if nextCursor == "" {
			return "", nil
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
