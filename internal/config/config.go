package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mjcramer/cortex/internal/logging"
)

type Config struct {
	BindAddr           string
	DefaultWaitTimeout time.Duration
	LogFormat          string // "json" or "text"
	LogLevel           slog.Level
	Slack              *SlackConfig
}

type SlackConfig struct {
	BotToken      string
	SigningSecret string
	ChannelPrefix string
	APIBaseURL    string
}

func FromEnv() (*Config, error) {
	bindAddr, err := serverAddr()
	if err != nil {
		return nil, err
	}

	timeoutSecs, err := parseUint64Env("CORTEX_DEFAULT_WAIT_TIMEOUT_SECONDS", 300)
	if err != nil {
		return nil, err
	}

	slack, err := slackConfigFromEnv()
	if err != nil {
		return nil, err
	}

	logFormat := strings.ToLower(os.Getenv("CORTEX_LOG_FORMAT"))
	if logFormat == "" {
		// Cloud Run aggregates structured JSON logs; default to JSON there and
		// to human-friendly text for local development.
		if os.Getenv("PORT") != "" {
			logFormat = "json"
		} else {
			logFormat = "text"
		}
	}
	if logFormat != "json" && logFormat != "text" && logFormat != "console" {
		return nil, fmt.Errorf("invalid CORTEX_LOG_FORMAT %q: want \"json\", \"text\", or \"console\"", logFormat)
	}

	logLevel, err := parseLogLevel(os.Getenv("CORTEX_LOG_LEVEL"))
	if err != nil {
		return nil, err
	}

	return &Config{
		BindAddr:           bindAddr,
		DefaultWaitTimeout: time.Duration(timeoutSecs) * time.Second,
		LogFormat:          logFormat,
		LogLevel:           logLevel,
		Slack:              slack,
	}, nil
}

// NewLogger builds a slog.Logger that honors the configured format and level.
// "json" uses slog's JSON handler (for Cloud Run log aggregation); "text" and
// "console" use the colorized Logback-style console handler.
func (c *Config) NewLogger() *slog.Logger {
	if c.LogFormat == "json" {
		h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: c.LogLevel})
		return slog.New(h)
	}
	return slog.New(logging.NewConsoleHandler(os.Stderr, c.LogLevel, shouldColor()))
}

// shouldColor decides whether to emit ANSI colors. CORTEX_LOG_COLOR=always|never
// forces the choice; otherwise color is on when stderr is a TTY and NO_COLOR is
// unset (https://no-color.org).
func shouldColor() bool {
	switch strings.ToLower(os.Getenv("CORTEX_LOG_COLOR")) {
	case "always", "1", "true":
		return true
	case "never", "0", "false":
		return false
	default:
		if os.Getenv("NO_COLOR") != "" {
			return false
		}
		return logging.IsTerminal(os.Stderr)
	}
}

func parseLogLevel(v string) (slog.Level, error) {
	switch strings.ToLower(v) {
	case "", "info":
		return slog.LevelInfo, nil
	case "trace", "debug":
		// slog has no trace level; "trace" is accepted as an alias for debug.
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid CORTEX_LOG_LEVEL %q: want trace, debug, info, warn, or error", v)
	}
}

func slackConfigFromEnv() (*SlackConfig, error) {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")

	switch {
	case botToken == "" && signingSecret == "":
		return nil, nil
	case botToken != "" && signingSecret != "":
		prefix := os.Getenv("SLACK_CHANNEL_PREFIX")
		if prefix == "" {
			prefix = "agent-"
		}
		apiBase := os.Getenv("SLACK_API_BASE_URL")
		if apiBase == "" {
			apiBase = "https://slack.com/api"
		}
		return &SlackConfig{
			BotToken:      botToken,
			SigningSecret: signingSecret,
			ChannelPrefix: prefix,
			APIBaseURL:    apiBase,
		}, nil
	default:
		return nil, errors.New("SLACK_BOT_TOKEN and SLACK_SIGNING_SECRET must either both be set or both be unset")
	}
}

func serverAddr() (string, error) {
	if v := os.Getenv("CORTEX_BIND_ADDR"); v != "" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return "", fmt.Errorf("invalid CORTEX_BIND_ADDR %q: %w", v, err)
		}
		return v, nil
	}

	cloudRunPort := os.Getenv("PORT")
	defaultHost := "127.0.0.1"
	if cloudRunPort != "" {
		defaultHost = "0.0.0.0"
	}

	host := os.Getenv("CORTEX_HOST")
	if host == "" {
		host = defaultHost
	}

	port := cloudRunPort
	if port == "" {
		port = os.Getenv("CORTEX_PORT")
	}
	if port == "" {
		port = "50051"
	}

	return net.JoinHostPort(host, port), nil
}

func parseUint64Env(name string, def uint64) (uint64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	parsed, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return parsed, nil
}
