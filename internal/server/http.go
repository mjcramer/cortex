package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
	"github.com/mjcramer/cortex/internal/slack"
)

type HTTPHandler struct {
	Sessions *sessions.Manager
	Slack    *slack.App // may be nil if slack is not configured
	Log      *slog.Logger
}

func NewHTTPHandler(sm *sessions.Manager, app *slack.App, logger *slog.Logger) *HTTPHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPHandler{Sessions: sm, Slack: app, Log: logger.With("component", "http")}
}

func (h *HTTPHandler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/slack/events", h.slackEvents)
	return mux
}

func (h *HTTPHandler) slackEvents(w http.ResponseWriter, r *http.Request) {
	if h.Slack == nil {
		http.Error(w, "Slack integration is not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if err := h.Slack.VerifyRequest(r.Header, body); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var envelope slack.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, fmt.Sprintf("invalid Slack event payload: %v", err), http.StatusBadRequest)
		return
	}

	switch envelope.Type {
	case "url_verification":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
	case "event_callback":
		var event slack.MessageEvent
		if len(envelope.Event) > 0 {
			if err := json.Unmarshal(envelope.Event, &event); err != nil {
				http.Error(w, fmt.Sprintf("invalid Slack message event: %v", err), http.StatusBadRequest)
				return
			}
		}
		h.handleReply(event)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (h *HTTPHandler) handleReply(event slack.MessageEvent) {
	reply := event.HumanThreadReply()
	if reply == nil {
		return
	}
	sessionID, ok := h.Sessions.FindBySlackThread(reply.Thread)
	if !ok {
		// A reply in a thread we don't track (e.g. unrelated channel chatter).
		return
	}
	h.Log.Debug("slack reply received",
		"session_id", sessionID, "responder", reply.UserID, "message", reply.Text)
	if err := h.Sessions.Submit(&pb.HumanReply{
		SessionId: sessionID,
		Response:  reply.Text,
		Responder: reply.UserID,
		Source:    fmt.Sprintf("slack:%s:%s", reply.Thread.ChannelID, reply.Thread.ThreadTS),
	}); err != nil {
		// Already-responded / not-found are benign races (duplicate Slack
		// deliveries, a session that timed out and was removed); log at debug.
		if errors.Is(err, sessions.ErrAlreadyResponded) || errors.Is(err, sessions.ErrNotFound) {
			h.Log.Debug("ignoring benign reply submission error",
				"session_id", sessionID, "error", err)
			return
		}
		h.Log.Error("failed to record human reply",
			"session_id", sessionID, "channel_id", reply.Thread.ChannelID,
			"thread_ts", reply.Thread.ThreadTS, "error", err)
		return
	}
	h.Log.Info("recorded human reply", "session_id", sessionID, "responder", reply.UserID)
}
