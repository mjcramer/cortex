package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/slack-go/slack/slackevents"

	"github.com/mjcramer/cortex/internal/agents"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
	"github.com/mjcramer/cortex/internal/slack"
)

type HTTPHandler struct {
	Sessions *sessions.Manager
	Slack    *slack.App // may be nil if slack is not configured
	Agents   *agents.Manager
	Commands *CommandsHandler
	Logger   *slog.Logger
}

func NewHTTPHandler(sm *sessions.Manager, app *slack.App, mgr *agents.Manager, cmds *CommandsHandler, logger *slog.Logger) *HTTPHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPHandler{Sessions: sm, Slack: app, Agents: mgr, Commands: cmds, Logger: logger}
}

func (h *HTTPHandler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/slack/events", h.slackEvents)
	mux.Handle("/slack/commands", h.Commands)
	return mux
}

func (h *HTTPHandler) slackEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if err := h.Slack.VerifyRequest(r.Header, body); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	event, err := slack.ParseEvent(body)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid Slack event payload: %v", err), http.StatusBadRequest)
		return
	}

	h.Logger.Debug("slack event received",
		"type", event.Type, "inner_type", event.InnerEvent.Type)

	switch event.Type {
	case slackevents.URLVerification:
		var challenge struct {
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &challenge); err != nil {
			http.Error(w, fmt.Sprintf("invalid url_verification payload: %v", err), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, challenge.Challenge)
	case slackevents.CallbackEvent:
		switch inner := event.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			h.handleMessage(inner)
		default:
			h.Logger.Debug("ignoring slack callback: not a message event",
				"inner_type", event.InnerEvent.Type)
		}
		w.WriteHeader(http.StatusOK)
	default:
		h.Logger.Debug("ignoring slack event: unhandled top-level type", "type", event.Type)
		w.WriteHeader(http.StatusOK)
	}
}

// handleMessage routes a Slack message event to either an agent (top-level
// messages in an agent's channel become turns in that agent's conversation)
// or, as a fallback, to a waiting gRPC session keyed by (channel_id, thread_ts).
func (h *HTTPHandler) handleMessage(event *slackevents.MessageEvent) {
	if event == nil {
		return
	}

	// Log the raw message event before any filtering, so a dropped message is
	// always visible (with the reason it was dropped) at debug.
	h.Logger.Debug("slack message event",
		"channel", event.Channel, "user", event.User, "subtype", event.SubType,
		"bot_id", event.BotID, "thread_ts", event.ThreadTimeStamp, "text", event.Text)

	if event.Type != "message" {
		h.Logger.Debug("ignoring slack message: not a message event", "type", event.Type)
		return
	}
	if event.SubType != "" || event.BotID != "" {
		h.Logger.Debug("ignoring slack message: bot or message subtype",
			"subtype", event.SubType, "bot_id", event.BotID)
		return
	}
	if event.Channel == "" || event.User == "" {
		h.Logger.Debug("ignoring slack message: missing channel or user")
		return
	}
	text := trimSpace(event.Text)
	if text == "" {
		h.Logger.Debug("ignoring slack message: empty text", "channel", event.Channel)
		return
	}

	// Top-level channel message → agent (if one owns this channel).
	if event.ThreadTimeStamp == "" && h.Agents != nil {
		if h.Agents.RouteMessage(event.Channel, agents.IncomingMessage{UserID: event.User, Text: text}) {
			h.Logger.Debug("routed slack message to agent",
				"channel", event.Channel, "user", event.User)
			return
		}
	}

	// Otherwise treat it as a threaded reply to a pending session.
	if event.ThreadTimeStamp == "" {
		h.Logger.Debug("ignoring slack message: no agent owns this channel and it is not a thread reply",
			"channel", event.Channel, "user", event.User)
		return
	}
	reply := slack.HumanThreadReply(event)
	if reply == nil {
		return
	}
	sessionID, ok := h.Sessions.FindBySlackThread(reply.Thread)
	if !ok {
		h.Logger.Debug("slack thread reply has no matching session",
			"channel", reply.Thread.ChannelID, "thread_ts", reply.Thread.ThreadTS)
		return
	}
	if err := h.Sessions.Submit(&pb.HumanReply{
		SessionId: sessionID,
		Response:  reply.Text,
		Responder: reply.UserID,
		Source:    fmt.Sprintf("slack:%s:%s", reply.Thread.ChannelID, reply.Thread.ThreadTS),
	}); err != nil {
		if errors.Is(err, sessions.ErrAlreadyResponded) || errors.Is(err, sessions.ErrNotFound) {
			h.Logger.Debug("ignoring benign reply submission error",
				"session_id", sessionID, "error", err)
			return
		}
		h.Logger.Error("failed to record human reply", "session_id", sessionID, "error", err)
		return
	}
	h.Logger.Info("recorded human reply", "session_id", sessionID, "responder", reply.UserID)
}

func trimSpace(s string) string {
	// Avoid importing strings just for one call in this file's hot path.
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
