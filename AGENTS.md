# Repository Guidelines

## Project Structure & Module Organization

Cortex is a Go service for routing agent events through Slack. The entry point is `cmd/cortex/main.go`. Core packages live under `internal/`: `server/` for gRPC and HTTP handlers, `sessions/` for response tracking, `slack/` and `slackadmin/` for Slack runtime and app registration, `agents/` and `claude/` for agent execution, `config/` for environment parsing, and `logging/` for structured logs. The gRPC contract is `proto/cortex.proto`; generated bindings are checked into `internal/cortexpb/`. Slack setup files are in `integrations/slack/`. Terraform infrastructure is in `terraform/`.

## Build, Test, and Development Commands

Use `make` as the primary interface:

- `make build`: builds `./bin/cortex`.
- `make run`: runs the local server on the configured host and port.
- `make test`: runs Go tests across `./...`.
- `make ci`: runs formatting checks, vet, and tests.
- `make fmt` / `make fmt-check`: format or verify Go source formatting.
- `make proto`: regenerates Go protobuf bindings from `proto/cortex.proto`.
- `make tunnel TUNNEL_PORT=23001`: opens an ngrok tunnel for Slack callbacks.

For focused work, direct Go commands are fine, such as `go test ./internal/sessions -run TestWaitsForResponse`.

## Coding Style & Naming Conventions

Follow standard Go style: tabs via `gofmt`, package-focused files, exported identifiers with clear names, and unexported helpers in lower camel case. Keep package names short and singular where practical. Do not edit generated files in `internal/cortexpb/` directly; update `proto/cortex.proto` and run `make proto`. Keep environment variable names uppercase and prefixed with `CORTEX_` when Cortex-specific.

## Testing Guidelines

Tests use Go's standard `testing` package and live beside implementation files as `*_test.go`. Prefer behavior-oriented names such as `TestWaitsForResponse` or `TestParseLogLevel`. Add focused unit tests for package logic and handler tests for request parsing, Slack signature paths, and gRPC behavior. Run `make test` during development and `make ci` before finishing changes.

## Commit & Pull Request Guidelines

Recent commits use short, imperative subject lines, for example `Fix Slack manifest registration and remove make slack-sync`. Keep commits scoped to one logical change. Pull requests should include a concise summary, test results such as `make ci`, any configuration or Terraform impact, and screenshots or Slack callback notes when behavior changes in Slack-facing flows.

## Security & Configuration Tips

Never commit Slack tokens, Anthropic API keys, Terraform backend secrets, or local token state files. Required runtime secrets include `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, and `ANTHROPIC_API_KEY`. Use `CORTEX_LOG_LEVEL=trace` only for local debugging because it logs full request bodies.
