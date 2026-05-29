package main

import (
	"context"
	"errors"
	"log"
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
		log.Fatalf("config: %v", err)
	}

	sm := sessions.NewManager()

	var (
		slackApp *slack.App
		notifier slack.Notifier = slack.DisabledNotifier{}
	)
	if cfg.Slack != nil {
		slackApp = slack.NewApp(cfg.Slack)
		notifier = slackApp
	}

	cortex := server.NewCortex(cfg, sm, notifier)
	httpHandler := server.NewHTTPHandler(sm, slackApp).Routes()

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

	log.Printf("cortex listening on %s (gRPC + HTTP via h2c)", cfg.BindAddr)
	if cfg.Slack != nil {
		log.Printf("slack events enabled; agent channels use prefix %q at /slack/events", cfg.Slack.ChannelPrefix)
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}
