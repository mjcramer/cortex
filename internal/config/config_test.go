package config

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	valid := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		"trace":   LevelTrace,
		"TRACE":   LevelTrace,
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range valid {
		got, err := parseLogLevel(in)
		if err != nil {
			t.Errorf("parseLogLevel(%q) returned error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}

	if _, err := parseLogLevel("nonsense"); err == nil {
		t.Error("parseLogLevel(\"nonsense\") expected an error, got nil")
	}
}

func TestLevelTraceBelowDebug(t *testing.T) {
	if LevelTrace >= slog.LevelDebug {
		t.Fatalf("LevelTrace (%d) must be below slog.LevelDebug (%d)", LevelTrace, slog.LevelDebug)
	}
}
