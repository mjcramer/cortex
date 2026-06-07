package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newTestLogger(buf *bytes.Buffer, level slog.Leveler) *slog.Logger {
	return slog.New(NewConsoleHandler(buf, level, false))
}

func TestConsoleHandlerFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewConsoleHandler(&buf, slog.LevelInfo, false)
	rec := slog.NewRecord(time.Date(2026, 6, 7, 18, 31, 29, 147000000, time.UTC), slog.LevelInfo, "agent event received", 0)
	rec.Add("session_id", "s1", "repo", "acme/api")
	if err := h.WithAttrs([]slog.Attr{slog.String("component", "grpc")}).Handle(t.Context(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := buf.String()
	want := "INFO  2026-06-07 18:31:29.147 grpc         agent event received session_id=s1 repo=acme/api\n"
	if got != want {
		t.Errorf("console line mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConsoleHandlerQuotesAndEscapes(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf, slog.LevelDebug).With("component", "grpc")
	log.Debug("agent message", "message", "line one\nline two")

	got := buf.String()
	if !strings.Contains(got, `message="line one\nline two"`) {
		t.Errorf("expected quoted/escaped multiline message, got: %q", got)
	}
	if strings.Count(got, "\n") != 1 { // only the trailing record newline
		t.Errorf("record should be a single line, got %d newlines: %q", strings.Count(got, "\n"), got)
	}
}

func TestConsoleHandlerEnabled(t *testing.T) {
	var buf bytes.Buffer
	h := NewConsoleHandler(&buf, slog.LevelInfo, false)
	if h.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("debug should be disabled at info level")
	}
	if !h.Enabled(t.Context(), slog.LevelError) {
		t.Error("error should be enabled at info level")
	}
}

func TestConsoleHandlerNoColorHasNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf, slog.LevelInfo).With("component", "grpc")
	log.Error("boom", "error", "kaboom")
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("color disabled but ANSI escape present: %q", buf.String())
	}
}
