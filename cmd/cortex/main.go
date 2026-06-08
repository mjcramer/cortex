package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"

	"github.com/mjcramer/cortex/internal/agents"
	"github.com/mjcramer/cortex/internal/claude"
	"github.com/mjcramer/cortex/internal/config"
	pb "github.com/mjcramer/cortex/internal/cortexpb"
	"github.com/mjcramer/cortex/internal/logging"
	"github.com/mjcramer/cortex/internal/server"
	"github.com/mjcramer/cortex/internal/sessions"
	"github.com/mjcramer/cortex/internal/slack"
	"github.com/mjcramer/cortex/internal/slackadmin"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		os.Stderr.WriteString("config: " + err.Error() + "\n")
		os.Exit(1)
	}

	logger := logging.New(os.Stderr, cfg.LogLevel)
	slog.SetDefault(logger)

	slackApp := slack.NewApp(cfg.Slack, logger)

	authCtx, authCancel := context.WithTimeout(context.Background(), 10*time.Second)
	team, user, err := slackApp.VerifyAuth(authCtx)
	authCancel()
	if err != nil {
		logger.Error("slack auth.test failed at startup", "err", err)
		os.Exit(1)
	}
	logger.Info("slack auth verified", "team", team, "bot_user", user)

	sm := sessions.NewManager()
	thinker := claude.NewThinker(cfg.Claude, logger)

	store, err := agents.NewStore(cfg.StateDir)
	if err != nil {
		logger.Error("init agent state store", "err", err)
		os.Exit(1)
	}
	if cfg.StateDir == "" {
		logger.Info("agent persistence disabled", "backend", "noop", "hint", "set CORTEX_STATE_DIR to enable")
	} else {
		logger.Info("agent persistence enabled", "backend", "file", "dir", cfg.StateDir)
	}
	mgr := agents.NewManager(slackApp, slackApp, thinker, store, logger)
	if err := mgr.Restore(context.Background()); err != nil {
		logger.Error("restore agents from store", "err", err)
		os.Exit(1)
	}
	commands := server.NewCommandsHandler(slackApp, mgr, logger)

	cortex := server.NewCortex(cfg, sm, slackApp, logger)
	httpRouter := server.NewHTTPHandler(sm, slackApp, mgr, commands, logger).Routes()
	httpHandler := server.HTTPRequestLogger(logger)(httpRouter)

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(server.UnaryGRPCLogger(logger)),
	)
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

	logger.Info("cortex listening",
		"addr", cfg.BindAddr,
		"log_level", logging.ParseLevel(cfg.LogLevel).String(),
		"channel_prefix", cfg.Slack.ChannelPrefix,
		"claude_model", cfg.Claude.Model,
		"events_path", "/slack/events",
		"commands_path", "/slack/commands",
		"auto_register", cfg.Register != nil,
	)

	serveErr := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	// Auto-register the Slack manifest once the listener is up. This must run
	// after the server is accepting connections: pushing event_subscriptions
	// makes Slack immediately POST a signed challenge to /slack/events, which
	// only succeeds if we're already serving.
	if cfg.Register != nil {
		go registerSlack(ctx, cfg, logger)
	}

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			logger.Error("server stopped with error", "err", err)
			os.Exit(1)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	grpcServer.GracefulStop()
	mgr.Shutdown()
	_ = srv.Shutdown(shutdownCtx)
}

// registerSlack waits for the local server to start serving, then pushes the
// manifest to Slack so its request URLs point at this instance. Failures are
// logged but never fatal — the server keeps running so you can fix creds or the
// callback URL and restart.
func registerSlack(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	logger.Debug("slack auto-register: waiting for local server to accept connections", "addr", cfg.BindAddr)
	if err := waitForReady(ctx, cfg.BindAddr); err != nil {
		logger.Error("slack auto-register skipped: server not ready", "err", err)
		return
	}
	logger.Debug("slack auto-register: server ready, pushing manifest")
	regCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := slackadmin.Register(regCtx, *cfg.Register, logger); err != nil {
		logger.Error("slack auto-register failed", "err", err)
	}
}

// waitForReady polls the local /healthz endpoint until it responds 200 or the
// context is cancelled, so registration only fires once Slack can reach us.
func waitForReady(ctx context.Context, bindAddr string) error {
	dialAddr := bindAddr
	if host, port, err := net.SplitHostPort(bindAddr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			dialAddr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	url := "http://" + dialAddr + "/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("server not ready at %s within timeout", dialAddr)
		case <-ticker.C:
		}
	}
}
