package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mjcramer/cortex/internal/slackadmin"
)

type Config struct {
	BindAddr           string
	DefaultWaitTimeout time.Duration
	LogLevel           string
	// StateDir is where runtime state is persisted. Empty disables durable
	// persistence. Set via CORTEX_STATE_DIR.
	StateDir string
	Slack    *SlackConfig
	Claude   *ClaudeConfig
	// Register is non-nil only when CORTEX_SLACK_AUTOREGISTER is enabled.
	Register *slackadmin.RegisterConfig
}

type ClaudeConfig struct {
	APIKey string
	Model  string
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
	claude, err := claudeConfigFromEnv()
	if err != nil {
		return nil, err
	}
	register, err := registerConfigFromEnv(slack.APIBaseURL)
	if err != nil {
		return nil, err
	}

	return &Config{
		BindAddr:           bindAddr,
		DefaultWaitTimeout: time.Duration(timeoutSecs) * time.Second,
		LogLevel:           os.Getenv("CORTEX_LOG_LEVEL"),
		StateDir:           os.Getenv("CORTEX_STATE_DIR"),
		Slack:              slack,
		Claude:             claude,
		Register:           register,
	}, nil
}

// registerConfigFromEnv builds the Slack auto-registration config when
// CORTEX_SLACK_AUTOREGISTER is truthy. It returns (nil, nil) when disabled so
// callers can simply check for a nil Register.
func registerConfigFromEnv(apiBase string) (*slackadmin.RegisterConfig, error) {
	if !parseBoolEnv("CORTEX_SLACK_AUTOREGISTER") {
		return nil, nil
	}
	appID := os.Getenv("SLACK_APP_ID")
	callbackURL := os.Getenv("SLACK_CALLBACK_URL")
	if appID == "" || callbackURL == "" {
		return nil, errors.New("CORTEX_SLACK_AUTOREGISTER=true requires SLACK_APP_ID and SLACK_CALLBACK_URL")
	}
	tokensPath := os.Getenv("CORTEX_SLACK_TOKENS_PATH")
	if tokensPath == "" {
		tokensPath = defaultTokensPath()
	}
	manifestPath := os.Getenv("CORTEX_SLACK_MANIFEST_PATH")
	if manifestPath == "" {
		manifestPath = filepath.Join("integrations", "slack", "manifest.yaml")
	}
	return &slackadmin.RegisterConfig{
		AppID:        appID,
		CallbackURL:  callbackURL,
		RefreshToken: os.Getenv("SLACK_CONFIG_REFRESH_TOKEN"),
		TokensPath:   tokensPath,
		ManifestPath: manifestPath,
		APIBaseURL:   apiBase,
	}, nil
}

// defaultTokensPath resolves $XDG_CONFIG_HOME/cortex/slack-tokens.json, falling
// back to ~/.config when XDG_CONFIG_HOME is unset.
func defaultTokensPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(dir, "cortex", "slack-tokens.json")
}

func parseBoolEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func claudeConfigFromEnv() (*ClaudeConfig, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is required")
	}
	model := os.Getenv("CORTEX_CLAUDE_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &ClaudeConfig{APIKey: apiKey, Model: model}, nil
}

func slackConfigFromEnv() (*SlackConfig, error) {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")

	if botToken == "" || signingSecret == "" {
		return nil, errors.New("SLACK_BOT_TOKEN and SLACK_SIGNING_SECRET are required")
	}

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
		port = "23001"
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
