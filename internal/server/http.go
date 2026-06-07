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
		if inner, ok := event.InnerEvent.Data.(*slackevents.MessageEvent); ok {
			h.handleMessage(inner)
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// handleMessage routes a Slack message event to either an agent (top-level
// messages in an agent's channel become turns in that agent's conversation)
// or, as a fallback, to a waiting gRPC session keyed by (channel_id, thread_ts).
func (h *HTTPHandler) handleMessage(event *slackevents.MessageEvent) {
	if event == nil || event.Type != "message" {
		return
	}
	if event.SubType != "" || event.BotID != "" {
		return
	}
	if event.Channel == "" || event.User == "" {
		return
	}
	text := trimSpace(event.Text)
	if text == "" {
		return
	}

	// Top-level channel message → agent (if one owns this channel).
	if event.ThreadTimeStamp == "" && h.Agents != nil {
		if h.Agents.RouteMessage(event.Channel, agents.IncomingMessage{UserID: event.User, Text: text}) {
			return
		}
	}

	// Otherwise treat it as a threaded reply to a pending session.
	if event.ThreadTimeStamp == "" {
		return
	}
	reply := slack.HumanThreadReply(event)
	if reply == nil {
		return
	}
	sessionID, ok := h.Sessions.FindBySlackThread(reply.Thread)
	if !ok {
		return
	}
	if err := h.Sessions.Submit(&pb.HumanReply{
		SessionId: sessionID,
		Response:  reply.Text,
		Responder: reply.UserID,
		Source:    fmt.Sprintf("slack:%s:%s", reply.Thread.ChannelID, reply.Thread.ThreadTS),
	}); err != nil {
		if !errors.Is(err, sessions.ErrAlreadyResponded) && !errors.Is(err, sessions.ErrNotFound) {
			// no logger here yet; drop silently
		}
	}
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
