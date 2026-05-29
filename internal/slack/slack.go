package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mjcramer/cortex/internal/config"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
)

type Notifier interface {
	Notify(ctx context.Context, signal *pb.AgentSignal) (sessions.ThreadRef, error)
}

type DisabledNotifier struct{}

func (DisabledNotifier) Notify(context.Context, *pb.AgentSignal) (sessions.ThreadRef, error) {
	return sessions.ThreadRef{}, errors.New("slack is not configured")
}

type App struct {
	cfg    *config.SlackConfig
	client *http.Client

	mu       sync.RWMutex
	channels map[string]string // agent name -> channel id
}

func NewApp(cfg *config.SlackConfig) *App {
	return &App{
		cfg:      cfg,
		client:   &http.Client{Timeout: 10 * time.Second},
		channels: make(map[string]string),
	}
}

func (a *App) ChannelNameFor(agent string) string {
	return a.cfg.ChannelPrefix + sanitizeChannelName(agent)
}

func (a *App) Notify(ctx context.Context, signal *pb.AgentSignal) (sessions.ThreadRef, error) {
	channelID, err := a.ensureChannel(ctx, signal.Agent)
	if err != nil {
		return sessions.ThreadRef{}, err
	}

	body := map[string]any{
		"channel":      channelID,
		"text":         formatSignalMessage(signal),
		"unfurl_links": false,
		"unfurl_media": false,
		"username":     signal.Agent,
	}

	var resp struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}

	if err := a.postJSON(ctx, "/chat.postMessage", body, &resp); err != nil {
		return sessions.ThreadRef{}, err
	}
	if !resp.OK {
		return sessions.ThreadRef{}, slackError("chat.postMessage", resp.Error)
	}
	if resp.Channel == "" || resp.TS == "" {
		return sessions.ThreadRef{}, errors.New("slack chat.postMessage response missing channel or ts")
	}

	return sessions.ThreadRef{ChannelID: resp.Channel, ThreadTS: resp.TS}, nil
}

func (a *App) VerifyRequest(headers http.Header, body []byte) error {
	signature := headers.Get("X-Slack-Signature")
	timestampHeader := headers.Get("X-Slack-Request-Timestamp")
	if signature == "" || timestampHeader == "" {
		return errors.New("missing slack signature headers")
	}

	timestamp, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return errors.New("invalid slack timestamp")
	}

	now := time.Now().Unix()
	if delta := now - timestamp; delta < -300 || delta > 300 {
		return errors.New("stale slack request timestamp")
	}

	mac := hmac.New(sha256.New, []byte(a.cfg.SigningSecret))
	fmt.Fprintf(mac, "v0:%d:", timestamp)
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("invalid slack request signature")
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
	id, err := a.createChannel(ctx, name)
	if err != nil {
		if err.Error() == "name_taken" {
			id, lookupErr := a.lookupChannel(ctx, name)
			if lookupErr != nil {
				return "", lookupErr
			}
			if id == "" {
				return "", fmt.Errorf("slack reports %q is taken but it was not listable", name)
			}
			if err := a.joinChannel(ctx, id); err != nil {
				return "", err
			}
			a.cacheChannel(agent, id)
			return id, nil
		}
		return "", err
	}

	a.cacheChannel(agent, id)
	return id, nil
}

func (a *App) cacheChannel(agent, id string) {
	a.mu.Lock()
	a.channels[agent] = id
	a.mu.Unlock()
}

func (a *App) createChannel(ctx context.Context, name string) (string, error) {
	body := map[string]any{"name": name, "is_private": false}
	var resp struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := a.postJSON(ctx, "/conversations.create", body, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		if resp.Error == "" {
			return "", errors.New("conversations.create returned ok=false")
		}
		return "", errors.New(resp.Error)
	}
	if resp.Channel.ID == "" {
		return "", errors.New("conversations.create returned ok without a channel id")
	}
	return resp.Channel.ID, nil
}

func (a *App) lookupChannel(ctx context.Context, name string) (string, error) {
	cursor := ""
	for {
		params := url.Values{}
		params.Set("exclude_archived", "true")
		params.Set("limit", "200")
		params.Set("types", "public_channel")
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var resp struct {
			OK       bool   `json:"ok"`
			Error    string `json:"error"`
			Channels []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channels"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}

		if err := a.getJSON(ctx, "/conversations.list", params, &resp); err != nil {
			return "", err
		}
		if !resp.OK {
			return "", slackError("conversations.list", resp.Error)
		}
		for _, c := range resp.Channels {
			if c.Name == name {
				return c.ID, nil
			}
		}
		if resp.ResponseMetadata.NextCursor == "" {
			return "", nil
		}
		cursor = resp.ResponseMetadata.NextCursor
	}
}

func (a *App) joinChannel(ctx context.Context, channelID string) error {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := a.postJSON(ctx, "/conversations.join", map[string]any{"channel": channelID}, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return slackError("conversations.join", resp.Error)
	}
	return nil
}

func (a *App) postJSON(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.APIBaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.BotToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return a.do(req, path, out)
}

func (a *App) getJSON(ctx context.Context, path string, params url.Values, out any) error {
	endpoint := a.cfg.APIBaseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.BotToken)
	return a.do(req, path, out)
}

func (a *App) do(req *http.Request, path string, out any) error {
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("call slack %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read slack %s response: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode slack %s response: %w", path, err)
	}
	return nil
}

func slackError(op, msg string) error {
	if msg == "" {
		return fmt.Errorf("%s returned ok=false", op)
	}
	return errors.New(msg)
}

// MessageEvent mirrors the subset of Slack's `message` event we care about.
type MessageEvent struct {
	Type     string `json:"type"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts"`
	TS       string `json:"ts"`
	Subtype  string `json:"subtype"`
	BotID    string `json:"bot_id"`
}

// Envelope is the outer Slack event payload.
type Envelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Event     json.RawMessage `json:"event"`
}

type ThreadReply struct {
	Thread sessions.ThreadRef
	UserID string
	Text   string
}

func (e MessageEvent) HumanThreadReply() *ThreadReply {
	if e.Type != "message" {
		return nil
	}
	if e.Subtype != "" || e.BotID != "" {
		return nil
	}
	if e.Channel == "" || e.ThreadTS == "" || e.User == "" {
		return nil
	}
	text := strings.TrimSpace(e.Text)
	if text == "" {
		return nil
	}
	return &ThreadReply{
		Thread: sessions.ThreadRef{ChannelID: e.Channel, ThreadTS: e.ThreadTS},
		UserID: e.User,
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
