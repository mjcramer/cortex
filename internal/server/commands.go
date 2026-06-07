package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/mjcramer/cortex/internal/agents"
	"github.com/mjcramer/cortex/internal/slack"
)

// CommandsHandler serves Slack slash command callbacks at /slack/commands.
type CommandsHandler struct {
	Slack  *slack.App
	Agents *agents.Manager
	Logger *slog.Logger
}

func NewCommandsHandler(slackApp *slack.App, mgr *agents.Manager, logger *slog.Logger) *CommandsHandler {
	return &CommandsHandler{Slack: slackApp, Agents: mgr, Logger: logger}
}

func (h *CommandsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if err := h.Slack.VerifyRequest(r.Header, body); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid form body: %v", err), http.StatusBadRequest)
		return
	}

	cmd := slashCommand{
		Command:     form.Get("command"),
		Text:        strings.TrimSpace(form.Get("text")),
		UserID:      form.Get("user_id"),
		UserName:    form.Get("user_name"),
		ChannelID:   form.Get("channel_id"),
		ResponseURL: form.Get("response_url"),
	}

	resp := h.dispatch(r.Context(), cmd)
	respondJSON(w, resp)
}

type slashCommand struct {
	Command     string
	Text        string
	UserID      string
	UserName    string
	ChannelID   string
	ResponseURL string
}

type slashResponse struct {
	ResponseType string `json:"response_type"` // "ephemeral" or "in_channel"
	Text         string `json:"text"`
}

func ephemeral(format string, args ...any) slashResponse {
	return slashResponse{ResponseType: "ephemeral", Text: fmt.Sprintf(format, args...)}
}

func inChannel(format string, args ...any) slashResponse {
	return slashResponse{ResponseType: "in_channel", Text: fmt.Sprintf(format, args...)}
}

// dispatch parses the slash command text and routes to the right handler.
// Slash command responses must return within 3 seconds; create/destroy are
// fast enough today (channel create + spawn goroutine, or archive) that we
// do them synchronously and skip the response_url two-step.
func (h *CommandsHandler) dispatch(ctx context.Context, cmd slashCommand) slashResponse {
	fields := strings.Fields(cmd.Text)
	if len(fields) == 0 || fields[0] == "help" {
		return ephemeral("%s", usageHint())
	}

	if fields[0] != "agent" || len(fields) < 2 {
		return ephemeral("unknown command. %s", usageHint())
	}

	switch fields[1] {
	case "create":
		if len(fields) != 3 {
			return ephemeral("usage: `/cortex agent create <name>`")
		}
		return h.handleCreate(ctx, cmd, fields[2])
	case "destroy":
		if len(fields) != 3 {
			return ephemeral("usage: `/cortex agent destroy <name>`")
		}
		return h.handleDestroy(ctx, cmd, fields[2])
	case "list":
		return h.handleList()
	default:
		return ephemeral("unknown agent subcommand `%s`. %s", fields[1], usageHint())
	}
}

func usageHint() string {
	return "usage: `/cortex agent create <name>` | `/cortex agent destroy <name>` | `/cortex agent list`"
}

var validAgentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,30}$`)

func (h *CommandsHandler) handleCreate(ctx context.Context, cmd slashCommand, name string) slashResponse {
	if !validAgentName.MatchString(name) {
		return ephemeral("invalid agent name `%s`. Use letters, digits, `-`, `_` (max 31 chars).", name)
	}

	channelID, err := h.Agents.Create(ctx, name)
	if err != nil {
		if errors.Is(err, agents.ErrAgentExists) {
			return ephemeral("agent `%s` already exists in <#%s>", name, channelID)
		}
		h.Logger.Error("agent create failed", "name", name, "err", err)
		return ephemeral("failed to create agent `%s`: %v", name, err)
	}

	// Announce in the destination channel too so the channel doesn't look empty.
	go func() {
		_ = h.Slack.PostToChannel(context.Background(), channelID,
			fmt.Sprintf(":wave: Agent `%s` reporting in. Talk to me here.", name))
	}()

	return inChannel("created agent `%s` in <#%s>", name, channelID)
}

func (h *CommandsHandler) handleDestroy(ctx context.Context, cmd slashCommand, name string) slashResponse {
	if err := h.Agents.Destroy(ctx, name); err != nil {
		if errors.Is(err, agents.ErrAgentNotFound) {
			return ephemeral("no agent named `%s`", name)
		}
		h.Logger.Error("agent destroy failed", "name", name, "err", err)
		return ephemeral("failed to destroy agent `%s`: %v", name, err)
	}
	return inChannel("destroyed agent `%s`", name)
}

func (h *CommandsHandler) handleList() slashResponse {
	names := h.Agents.List()
	if len(names) == 0 {
		return ephemeral("no agents are currently running.")
	}
	return ephemeral("active agents: `%s`", strings.Join(names, "`, `"))
}

func respondJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
