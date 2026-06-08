package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestHandlerPrettyPrintsJSONBody(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(&out, LevelTrace, false))

	logger.LogAttrs(context.Background(), LevelTrace, "http.request.trace",
		slog.String("method", "POST"),
		slog.String("path", "/slack/events"),
		slog.String("body", `{"type":"event_callback","event":{"text":"hello","channel":"C123"}}`),
	)

	got := out.String()
	for _, want := range []string{
		"TRACE",
		"\n    method = POST\n",
		"= /slack/events\n",
		"  body:\n",
		`    "type": "event_callback"`,
		`      "text": "hello"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `body="{\"`) {
		t.Fatalf("body was escaped inline instead of pretty-printed:\n%s", got)
	}
}

func TestHandlerRendersAttrsOnePerLine(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(&out, slog.LevelDebug, false))

	logger.LogAttrs(context.Background(), slog.LevelDebug, "http.request",
		slog.String("method", "GET"),
		slog.String("path", "/healthz"),
		slog.Int("status", 200),
	)

	got := out.String()
	// The old inline rendering must be gone.
	if strings.Contains(got, "method=GET") {
		t.Fatalf("attrs should no longer be inline:\n%s", got)
	}
	// Each attribute on its own indented, key-aligned line. method and status
	// are the widest keys (6), so they sit exactly one space before '='.
	for _, want := range []string{"\n    method = GET\n", "\n    status = 200\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing line %q in:\n%s", want, got)
		}
	}
	// The shorter key is padded to align, still on its own line.
	if !strings.Contains(got, "\n    path ") || !strings.Contains(got, "= /healthz\n") {
		t.Fatalf("path not rendered on its own aligned line:\n%s", got)
	}
}
