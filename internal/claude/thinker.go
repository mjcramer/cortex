package claude

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/mjcramer/cortex/internal/agents"
	"github.com/mjcramer/cortex/internal/config"
)

// Thinker implements agents.Thinker by routing each turn through the
// Anthropic Messages API. One Thinker is shared across all agents; the
// per-agent system prompt is constructed at call time.
type Thinker struct {
	client    anthropic.Client
	model     anthropic.Model
	maxTokens int64
	log       *slog.Logger
}

func NewThinker(cfg *config.ClaudeConfig, logger *slog.Logger) *Thinker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Thinker{
		client:    anthropic.NewClient(option.WithAPIKey(cfg.APIKey)),
		model:     anthropic.Model(cfg.Model),
		maxTokens: 1024,
		log:       logger.With("component", "claude"),
	}
}

func (t *Thinker) Respond(ctx context.Context, agentName string, history []agents.Turn, incoming agents.IncomingMessage) (string, error) {
	msgs := make([]anthropic.MessageParam, 0, len(history)+1)
	for _, turn := range history {
		switch turn.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(turn.Text)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(turn.Text)))
		}
	}
	msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(incoming.Text)))

	t.log.Debug("claude request →",
		"agent", agentName, "model", string(t.model),
		"history_turns", len(history), "prompt", incoming.Text)

	resp, err := t.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     t.model,
		MaxTokens: t.maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt(agentName)},
		},
		Messages: msgs,
	})
	if err != nil {
		t.log.Error("claude request failed", "agent", agentName, "err", err)
		return "", fmt.Errorf("anthropic messages.new: %w", err)
	}

	var sb strings.Builder
	for _, block := range resp.Content {
		if tb := block.AsText(); tb.Type == "text" {
			sb.WriteString(tb.Text)
		}
	}

	out := strings.TrimSpace(sb.String())
	t.log.Debug("claude response ←",
		"agent", agentName,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"text", out)
	if out == "" {
		return "_(no response)_", nil
	}
	return out, nil
}

func systemPrompt(agent string) string {
	return fmt.Sprintf(
		"You are %s, an AI assistant operating inside a Slack channel named #agent-%s. "+
			"You're talking with the team in real time. Keep responses concise and conversational; "+
			"use Slack markdown when it helps (e.g. `code`, *bold*). "+
			"Do not preface your replies with your name — Slack already shows it.",
		agent, agent,
	)
}
