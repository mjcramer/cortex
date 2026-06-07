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
		"method=POST",
		"path=/slack/events",
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

func TestHandlerKeepsSmallAttrsInline(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(&out, slog.LevelDebug, false))

	logger.LogAttrs(context.Background(), slog.LevelDebug, "http.request",
		slog.String("method", "GET"),
		slog.String("path", "/healthz"),
		slog.Int("status", 200),
	)

	got := out.String()
	if !strings.Contains(got, "method=GET path=/healthz status=200") {
		t.Fatalf("small attrs should stay inline:\n%s", got)
	}
	if strings.Contains(got, "\n  method:") {
		t.Fatalf("small attrs were rendered as blocks:\n%s", got)
	}
}
