// Package logging provides a human-friendly, colorized console slog.Handler
// modeled on the project's original Logback console pattern:
//
//	%highlight(%-5level) %boldWhite(%d{yyyy-MM-dd HH:mm:ss.SSS}) %-50.50logger %message
//
// The "logger" maps to the slog "component" attribute (e.g. grpc, slack, http).
// JSON output is handled separately by slog's JSONHandler for production.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ANSI escape codes used to mimic the Logback %highlight / %boldWhite / %boldRed
// conversion words.
const (
	ansiReset     = "\x1b[0m"
	ansiBoldRed   = "\x1b[1;31m"
	ansiRed       = "\x1b[31m"
	ansiYellow    = "\x1b[33m"
	ansiBlue      = "\x1b[34m"
	ansiBoldWhite = "\x1b[1;37m"
)

// loggerWidth is the fixed width of the component column. The original Logback
// pattern used 50 to fit fully-qualified Java logger names; Cortex components
// (grpc, slack, http, config) are short, so a narrower column keeps messages
// aligned without a wall of padding.
const loggerWidth = 12

const timeFormat = "2006-01-02 15:04:05.000" // yyyy-MM-dd HH:mm:ss.SSS

// ConsoleHandler renders records in the Logback-style console layout.
type ConsoleHandler struct {
	mu        *sync.Mutex
	w         io.Writer
	level     slog.Leveler
	color     bool
	component string
	attrs     []slog.Attr
	groups    []string
}

// NewConsoleHandler builds a ConsoleHandler writing to w at the given minimum
// level. color toggles ANSI escapes.
func NewConsoleHandler(w io.Writer, level slog.Leveler, color bool) *ConsoleHandler {
	return &ConsoleHandler{mu: &sync.Mutex{}, w: w, level: level, color: color}
}

func (h *ConsoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *ConsoleHandler) WithAttrs(as []slog.Attr) slog.Handler {
	nh := h.clone()
	for _, a := range as {
		// At the top level the "component" attribute acts as the logger name.
		if len(nh.groups) == 0 && a.Key == "component" {
			nh.component = a.Value.Resolve().String()
			continue
		}
		nh.attrs = append(nh.attrs, qualify(nh.groups, a))
	}
	return nh
}

func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	nh.groups = append(nh.groups, name)
	return nh
}

func (h *ConsoleHandler) clone() *ConsoleHandler {
	c := *h
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	c.groups = append([]string(nil), h.groups...)
	return &c
}

func (h *ConsoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	// %highlight(%-5level)
	level := fmt.Sprintf("%-5s", levelLabel(r.Level))
	h.paint(&b, levelColor(r.Level), level)
	b.WriteByte(' ')

	// %boldWhite(%d{yyyy-MM-dd HH:mm:ss.SSS})
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	h.paint(&b, ansiBoldWhite, ts.Format(timeFormat))
	b.WriteByte(' ')

	// %-50.50logger  (component, padded/truncated)
	comp := h.component
	if comp == "" {
		comp = "-"
	}
	b.WriteString(padTrunc(comp, loggerWidth))
	b.WriteByte(' ')

	// %message
	b.WriteString(r.Message)

	// trailing key=value attributes
	for _, a := range h.attrs {
		h.appendAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(&b, qualify(h.groups, a))
		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *ConsoleHandler) appendAttr(b *strings.Builder, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	pair := a.Key + "=" + quoteIfNeeded(a.Value.String())
	// Errors stand out in red, echoing %boldRed(%exception).
	if a.Key == "error" {
		h.paint(b, ansiRed, pair)
		return
	}
	b.WriteString(pair)
}

// paint writes s wrapped in the given ANSI color when color is enabled and the
// color is non-empty (DEBUG is intentionally uncolored, matching Logback).
func (h *ConsoleHandler) paint(b *strings.Builder, color, s string) {
	if h.color && color != "" {
		b.WriteString(color)
		b.WriteString(s)
		b.WriteString(ansiReset)
		return
	}
	b.WriteString(s)
}

func levelLabel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// levelColor mirrors Logback's default %highlight mapping: ERROR bold red,
// WARN red/yellow, INFO blue, DEBUG (and below) uncolored.
func levelColor(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return ansiBoldRed
	case l >= slog.LevelWarn:
		return ansiYellow
	case l >= slog.LevelInfo:
		return ansiBlue
	default:
		return ""
	}
}

func padTrunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

func qualify(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		return a
	}
	a.Key = strings.Join(groups, ".") + "." + a.Key
	return a
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\r\n\"=") {
		return strconv.Quote(s)
	}
	return s
}

// IsTerminal reports whether f refers to a character device (a TTY), used to
// decide whether to enable ANSI colors by default.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
