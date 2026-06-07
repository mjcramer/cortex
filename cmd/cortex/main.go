package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"

	"github.com/mjcramer/cortex/internal/config"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/server"
	"github.com/mjcramer/cortex/internal/sessions"
	"github.com/mjcramer/cortex/internal/slack"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		// Config (and thus the logger) isn't available yet; fall back to the
		// default slog logger for this one fatal path.
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger := cfg.NewLogger()
	slog.SetDefault(logger)

	sm := sessions.NewManager()

	var (
		slackApp *slack.App
		notifier slack.Notifier = slack.DisabledNotifier{}
	)
	if cfg.Slack != nil {
		slackApp = slack.NewApp(cfg.Slack, logger)
		notifier = slackApp
	}

	cortex := server.NewCortex(cfg, sm, notifier, logger)
	httpHandler := server.NewHTTPHandler(sm, slackApp, logger).Routes()

	grpcServer := grpc.NewServer()
	pb.RegisterCortexAgentServiceServer(grpcServer, cortex)

	mixed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		httpHandler.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           h2c.NewHandler(mixed, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		grpcServer.GracefulStop()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("cortex listening", "addr", cfg.BindAddr, "transport", "grpc+http (h2c)")
	if cfg.Slack != nil {
		logger.Info("slack events enabled", "channel_prefix", cfg.Slack.ChannelPrefix, "events_path", "/slack/events")
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}
