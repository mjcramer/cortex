package server

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/mjcramer/cortex/internal/config"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/logging"
	"github.com/mjcramer/cortex/internal/sessions"
)

type stubNotifier struct{}

func (stubNotifier) Notify(context.Context, *pb.AgentSignal) (sessions.ThreadRef, error) {
	return sessions.ThreadRef{ChannelID: "C1", ThreadTS: "1.1"}, nil
}

func sendEventAt(t *testing.T, level slog.Level) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(logging.NewConsoleHandler(&buf, level, false))
	c := &Cortex{
		Cfg:      &config.Config{},
		Sessions: sessions.NewManager(),
		Notifier: stubNotifier{},
		Log:      logger.With("component", "grpc"),
	}
	if _, err := c.SendEvent(context.Background(), &pb.AgentSignal{
		Agent: "reviewer", SessionId: "s1", Message: "please review", Repo: "acme/api",
	}); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	return buf.String()
}

func TestSendEventLogsMessageAtDebug(t *testing.T) {
	out := sendEventAt(t, slog.LevelDebug)
	if !strings.Contains(out, "agent message") || !strings.Contains(out, "please review") {
		t.Errorf("debug level should log the agent message body, got:\n%s", out)
	}
}

func TestSendEventHidesMessageAtInfo(t *testing.T) {
	out := sendEventAt(t, slog.LevelInfo)
	if strings.Contains(out, "agent message") {
		t.Errorf("info level should NOT log the agent message body, got:\n%s", out)
	}
	if !strings.Contains(out, "agent event received") {
		t.Errorf("info level should still log the event, got:\n%s", out)
	}
}
