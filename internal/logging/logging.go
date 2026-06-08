package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LevelTrace is one step below slog.LevelDebug. We use it to log every
// incoming request, including its body, for live debugging sessions.
const LevelTrace = slog.Level(-8)

// New returns a slog.Logger that emits records using the project's logback-
// derived console format. When `out` is a TTY, ANSI colors are used; otherwise
// the output is plain text.
func New(out io.Writer, levelStr string) *slog.Logger {
	return slog.New(NewHandler(out, ParseLevel(levelStr), wantColor(out)))
}

func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "", "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Trace is a convenience wrapper since slog has no Trace shortcut. It builds
// the slog.Record manually so the source frame reflects the *caller's* file
// and line, not this helper.
func Trace(ctx context.Context, logger *slog.Logger, msg string, args ...any) {
	if !logger.Enabled(ctx, LevelTrace) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(2, pcs[:]) // skip runtime.Callers and Trace itself
	r := slog.NewRecord(time.Now(), LevelTrace, msg, pcs[0])
	r.Add(args...)
	_ = logger.Handler().Handle(ctx, r)
}

// ---------------------------------------------------------------------------
// Handler — port of:
//   %highlight(%-5level) %boldWhite(%d{yyyy-MM-dd HH:mm:ss.SSS})
//     %-25.25logger{25} %message%n%boldRed(%exception)
//
// Structured attrs are appended after the message (dimmed); `err`/`error`
// attrs are pulled out and printed on the following line in bold red.
// ---------------------------------------------------------------------------

const (
	ansiReset     = "\x1b[0m"
	ansiDim       = "\x1b[2m"
	ansiRed       = "\x1b[31m"
	ansiYellow    = "\x1b[33m"
	ansiBlue      = "\x1b[34m"
	ansiMagenta   = "\x1b[35m"
	ansiCyan      = "\x1b[36m"
	ansiBoldRed   = "\x1b[1;31m"
	ansiBoldWhite = "\x1b[1;37m"
)

type Handler struct {
	w     io.Writer
	level slog.Level
	color bool
	mu    *sync.Mutex

	attrs  []slog.Attr // attrs added via WithAttrs (already-formatted)
	groups []string
}

func NewHandler(w io.Writer, level slog.Level, color bool) *Handler {
	return &Handler{w: w, level: level, color: color, mu: &sync.Mutex{}}
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder

	// %highlight(%-5level)
	lvl := levelName(r.Level)
	if h.color {
		sb.WriteString(levelColor(r.Level))
	}
	fmt.Fprintf(&sb, "%-5s", lvl)
	if h.color {
		sb.WriteString(ansiReset)
	}
	sb.WriteByte(' ')

	// %boldWhite(%d{yyyy-MM-dd HH:mm:ss.SSS})
	if h.color {
		sb.WriteString(ansiBoldWhite)
	}
	sb.WriteString(r.Time.Format("2006-01-02 15:04:05.000"))
	if h.color {
		sb.WriteString(ansiReset)
	}
	sb.WriteByte(' ')

	// %-25.25logger{25} — source file path relative to module root
	source := sourceFor(r.PC)
	if len(source) > 25 {
		// Left-truncate so the most-specific part stays visible.
		source = source[len(source)-25:]
	}
	fmt.Fprintf(&sb, "%-25.25s", source)
	sb.WriteByte(' ')

	// %message
	sb.WriteString(r.Message)

	// Collect attrs (handler-attached + record-attached), pulling out err/error
	// and rendering bulky values as readable blocks instead of escaped inline text.
	type kv struct{ key, val string }
	var (
		errVal   string
		hasErr   bool
		pairs    []kv
		maxKey   int
		blockBuf strings.Builder
	)
	emit := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		if isErrKey(a.Key) {
			errVal = a.Value.String()
			hasErr = true
			return
		}
		if block, ok := formatBlockAttr(a); ok {
			blockBuf.WriteString(block)
			return
		}
		pairs = append(pairs, kv{a.Key, quoteIfNeeded(inlineValue(a.Value))})
		if len(a.Key) > maxKey {
			maxKey = len(a.Key)
		}
	}
	for _, a := range h.attrs {
		emit(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		emit(a)
		return true
	})

	// End the message line, then render each attribute on its own indented,
	// key-aligned line in the same dim/gray as before.
	sb.WriteByte('\n')
	for _, p := range pairs {
		if h.color {
			sb.WriteString(ansiDim)
		}
		fmt.Fprintf(&sb, "    %-*s = %s", maxKey, p.key, p.val)
		if h.color {
			sb.WriteString(ansiReset)
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(blockBuf.String())

	// %boldRed(%exception) on the following line, if present.
	if hasErr {
		if h.color {
			sb.WriteString(ansiBoldRed)
		}
		sb.WriteString("  ")
		sb.WriteString(errVal)
		if h.color {
			sb.WriteString(ansiReset)
		}
		sb.WriteByte('\n')
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write([]byte(sb.String()))
	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &nh
}

func (h *Handler) WithGroup(name string) slog.Handler {
	// Groups are flattened: we don't honor them in the output. Returning a
	// copy keeps the slog contract intact (callers can still attach attrs).
	nh := *h
	nh.groups = append(append([]string{}, h.groups...), name)
	return &nh
}

// ---------------------------------------------------------------------------

func levelName(l slog.Level) string {
	switch {
	case l <= LevelTrace:
		return "TRACE"
	case l <= slog.LevelDebug:
		return "DEBUG"
	case l <= slog.LevelInfo:
		return "INFO"
	case l <= slog.LevelWarn:
		return "WARN"
	default:
		return "ERROR"
	}
}

func levelColor(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return ansiBoldRed
	case l >= slog.LevelWarn:
		return ansiYellow
	case l >= slog.LevelInfo:
		return ansiBlue
	case l >= slog.LevelDebug:
		return ansiCyan
	default: // TRACE and below
		return ansiMagenta
	}
}

func isErrKey(k string) bool {
	return k == "err" || k == "error"
}

func inlineValue(v slog.Value) string {
	if v.Kind() == slog.KindString {
		return v.String()
	}
	if v.Kind() == slog.KindAny {
		return fmt.Sprint(v.Any())
	}
	return v.String()
}

func formatBlockAttr(a slog.Attr) (string, bool) {
	value, ok := blockValue(a)
	if !ok {
		return "", false
	}
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(a.Key)
	sb.WriteString(":\n")
	for _, line := range strings.Split(value, "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String(), true
}

func blockValue(a slog.Attr) (string, bool) {
	if a.Value.Kind() == slog.KindString {
		raw := a.Value.String()
		if pretty, ok := prettyJSONString(raw); ok {
			return pretty, true
		}
		if wantsBlock(a.Key, raw) {
			return raw, true
		}
		return "", false
	}

	if a.Value.Kind() == slog.KindAny {
		raw := fmt.Sprint(a.Value.Any())
		if pretty, ok := prettyJSONValue(a.Value.Any()); ok {
			return pretty, true
		}
		if wantsBlock(a.Key, raw) {
			return raw, true
		}
	}

	return "", false
}

func wantsBlock(key, value string) bool {
	switch key {
	case "body", "headers", "request", "response", "payload":
		return value != ""
	}
	return len(value) > 160 || strings.Contains(value, "\n")
}

func prettyJSONString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !(strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) {
		return "", false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(s), "", "  "); err != nil {
		return "", false
	}
	return out.String(), true
}

func prettyJSONValue(v any) (string, bool) {
	switch v.(type) {
	case nil, string, error:
		return "", false
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// quoteIfNeeded wraps a value in double-quotes when it would otherwise be
// ambiguous in `key=value` form (contains whitespace, `=`, or `"`).
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if !needsQuote(s) {
		return s
	}
	return strconv.Quote(s)
}

func needsQuote(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '=' {
			return true
		}
	}
	return false
}

// sourceFor returns "<relpath>:<line>" for the call site, using the path
// relative to the module root so the column stays narrow.
func sourceFor(pc uintptr) string {
	if pc == 0 {
		return "?"
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	file := frame.File
	if rel := relToModule(file); rel != "" {
		file = rel
	}
	return file + ":" + strconv.Itoa(frame.Line)
}

// moduleRoot is filled in once at init time by walking up from this file.
var moduleRoot = findModuleRoot()

func findModuleRoot() string {
	// Use the path of this source file as a starting point.
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func relToModule(file string) string {
	if moduleRoot == "" {
		return ""
	}
	if rel, err := filepath.Rel(moduleRoot, file); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return ""
}

// wantColor returns true if `w` is a terminal-attached file. Anything that
// isn't a *os.File (e.g. a *bytes.Buffer in tests) is treated as not-a-tty.
func wantColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
