package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	"github.com/mjcramer/cortex/internal/logging"
)

// HTTPRequestLogger returns middleware that logs every incoming HTTP request.
//
// At DEBUG: method, path, remote, status, duration.
// At TRACE: above + headers + the full request body (buffered, then restored
// so downstream handlers can still read it). Keep this off in production.
func HTTPRequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			var bodyBytes []byte
			if logger.Enabled(r.Context(), logging.LevelTrace) && r.Body != nil {
				bodyBytes, _ = io.ReadAll(r.Body)
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote", r.RemoteAddr),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
			}
			logger.LogAttrs(r.Context(), slog.LevelDebug, "http.request",
				attrsFromAny(attrs)...)

			if logger.Enabled(r.Context(), logging.LevelTrace) {
				traceAttrs := append(attrs,
					slog.Any("headers", flattenHeaders(r.Header)),
					slog.String("body", string(bodyBytes)),
				)
				logger.LogAttrs(r.Context(), logging.LevelTrace, "http.request.trace",
					attrsFromAny(traceAttrs)...)
			}
		})
	}
}

// UnaryGRPCLogger returns a grpc.UnaryServerInterceptor that logs every RPC.
//
// At DEBUG: method, peer, status, duration.
// At TRACE: above + the full request proto via slog's default formatting.
func UnaryGRPCLogger(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)

		peerAddr := ""
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			peerAddr = p.Addr.String()
		}

		attrs := []any{
			slog.String("method", info.FullMethod),
			slog.String("peer", peerAddr),
			slog.Duration("duration", time.Since(start)),
		}
		if err != nil {
			attrs = append(attrs, slog.String("error", err.Error()))
		}
		logger.LogAttrs(ctx, slog.LevelDebug, "grpc.request", attrsFromAny(attrs)...)

		if logger.Enabled(ctx, logging.LevelTrace) {
			traceAttrs := append(attrs, slog.Any("request", req))
			logger.LogAttrs(ctx, logging.LevelTrace, "grpc.request.trace",
				attrsFromAny(traceAttrs)...)
		}

		return resp, err
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		// Mask the signing header so logs don't accidentally leak the secret.
		if k == "X-Slack-Signature" && len(v) > 0 {
			out[k] = "v0=<redacted>"
			continue
		}
		if len(v) == 1 {
			out[k] = v[0]
		} else {
			out[k] = joinComma(v)
		}
	}
	return out
}

func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

func attrsFromAny(in []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(in))
	for _, v := range in {
		if a, ok := v.(slog.Attr); ok {
			out = append(out, a)
		}
	}
	return out
}
