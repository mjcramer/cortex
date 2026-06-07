package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mjcramer/cortex/internal/config"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/sessions"
	"github.com/mjcramer/cortex/internal/slack"
)

type Cortex struct {
	pb.UnimplementedCortexAgentServiceServer

	Cfg      *config.Config
	Sessions *sessions.Manager
	Notifier slack.Notifier
	Log      *slog.Logger
}

func NewCortex(cfg *config.Config, sm *sessions.Manager, n slack.Notifier, logger *slog.Logger) *Cortex {
	if logger == nil {
		logger = slog.Default()
	}
	return &Cortex{Cfg: cfg, Sessions: sm, Notifier: n, Log: logger.With("component", "grpc")}
}

func (c *Cortex) SendEvent(ctx context.Context, signal *pb.AgentSignal) (*pb.Ack, error) {
	if err := validateSignal(signal); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := c.Sessions.Register(signal.SessionId); err != nil {
		if errors.Is(err, sessions.ErrAlreadyExists) {
			return nil, status.Error(codes.AlreadyExists, "session_id is already active")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	log := c.Log.With("session_id", signal.SessionId, "agent", signal.Agent)
	log.Info("agent event received", "repo", signal.Repo)
	log.Debug("agent message", "message", signal.Message)

	thread, err := c.Notifier.Notify(ctx, signal)
	if err != nil {
		c.Sessions.Remove(signal.SessionId)
		log.Error("failed to notify human responder", "error", err)
		return nil, status.Errorf(codes.Unavailable, "failed to notify human responder: %v", err)
	}

	if err := c.Sessions.AttachSlackThread(signal.SessionId, thread); err != nil {
		c.Sessions.Remove(signal.SessionId)
		log.Error("failed to attach slack thread to session",
			"error", err, "channel_id", thread.ChannelID, "thread_ts", thread.ThreadTS)
		return nil, status.Errorf(codes.Internal, "failed to attach slack thread %s:%s to session: %v",
			thread.ChannelID, thread.ThreadTS, err)
	}

	log.Info("agent event posted to slack", "channel_id", thread.ChannelID, "thread_ts", thread.ThreadTS)

	return &pb.Ack{
		Accepted: true,
		Detail:   "event posted to slack thread " + thread.ChannelID + ":" + thread.ThreadTS,
	}, nil
}

func (c *Cortex) WaitForResponse(ctx context.Context, req *pb.SessionRequest) (*pb.HumanResponse, error) {
	if strings.TrimSpace(req.SessionId) == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	timeout := c.Cfg.DefaultWaitTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	return c.Sessions.WaitForResponse(ctx, req.SessionId, timeout), nil
}

func (c *Cortex) SubmitHumanResponse(_ context.Context, reply *pb.HumanReply) (*pb.Ack, error) {
	if err := validateReply(reply); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := c.Sessions.Submit(reply); err != nil {
		switch {
		case errors.Is(err, sessions.ErrNotFound):
			return nil, status.Error(codes.NotFound, "unknown session_id")
		case errors.Is(err, sessions.ErrAlreadyResponded):
			return nil, status.Error(codes.AlreadyExists, "a response has already been recorded")
		default:
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	return &pb.Ack{Accepted: true, Detail: "response recorded"}, nil
}

func validateSignal(s *pb.AgentSignal) error {
	if strings.TrimSpace(s.Agent) == "" {
		return errors.New("agent is required")
	}
	if strings.TrimSpace(s.SessionId) == "" {
		return errors.New("session_id is required")
	}
	if strings.TrimSpace(s.Message) == "" {
		return errors.New("message is required")
	}
	return nil
}

func validateReply(r *pb.HumanReply) error {
	if strings.TrimSpace(r.SessionId) == "" {
		return errors.New("session_id is required")
	}
	if strings.TrimSpace(r.Response) == "" {
		return errors.New("response is required")
	}
	return nil
}
