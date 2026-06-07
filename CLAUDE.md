# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Cortex is an AI agent manager hub that communicates via messaging apps. It is built in Go using gRPC for agent-to-service traffic, and is currently targeting a single-service MVP on Google Cloud Run.

## Build System

This project uses Make wrapping the Go toolchain.

### Common Commands

```bash
# Build the binary into ./bin/cortex
make build

# Build a stripped release binary
make release

# Run the server (gRPC + HTTP multiplexed on one h2c listener)
make run

# Run with explicit bind configuration
CORTEX_HOST=127.0.0.1 CORTEX_PORT=23001 make run

# Run tests
make test

# Run CI checks locally (gofmt-check, vet, tests)
make ci

# Format code
make fmt

# Check formatting without modifying
make fmt-check

# Regenerate protobuf bindings from proto/
make proto

# Open a public ngrok tunnel for Slack callbacks
make tunnel TUNNEL_PORT=23001
```

### Direct Go Commands

```bash
# Build all packages
go build ./...

# Run a specific test
go test ./internal/sessions -run TestWaitsForResponse

# Vet
go vet ./...
```

### Protobuf Compilation

`proto/cortex.proto` is the source of truth. `make proto` regenerates `internal/cortexpb/cortex.pb.go` and `internal/cortexpb/cortex_grpc.pb.go`. The generated files are checked in. Requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on PATH.

## Architecture

### gRPC Service Layer

The core service is defined in `proto/cortex.proto` and implements `CortexAgentService` with three RPC methods:

1. **SendEvent**: receives `AgentSignal` messages from agents containing agent name, session ID, repository, and message content
2. **WaitForResponse**: blocking call that waits for a human response based on session ID, with a bounded timeout
3. **SubmitHumanResponse**: records a human response for an existing session

The service is implemented in `internal/server/grpc.go` using an in-memory `sessions.Manager` (see `internal/sessions/`). Agent events are posted into Slack as normal messages; human replies arrive through Slack Events API message callbacks handled at `POST /slack/events` in `internal/server/http.go`.

`cmd/cortex/main.go` runs gRPC and HTTP on the same TCP listener via `h2c` — application/grpc requests go to the gRPC server, everything else hits the HTTP mux. This keeps Cloud Run single-port deployment trivial.

### Slack Identity Model

One Slack app, one channel per agent (`#<prefix><agent>`). The Slack client wraps [`github.com/slack-go/slack`](https://github.com/slack-go/slack) (signature verification, conversations API, events parsing). On the first `SendEvent` for a given agent:

1. `slack.App.ensureChannel` checks the per-agent cache, then calls `client.CreateConversationContext`. If Slack returns `name_taken`, it falls back to `GetConversationsContext` + `JoinConversationContext`.
2. The bot posts via `client.PostMessageContext` with `slack.MsgOptionUsername(agent)` so each agent presents a distinct identity in Slack without a separate Slack app per agent.
3. The resulting `(channel_id, thread_ts)` is stored against the session, and inbound `message` events route by that pair.

### Server Configuration

**Required at startup** — the server exits non-zero if any are missing, and the Slack token is verified via `auth.test` so an invalid/revoked token also fails the boot:

- `SLACK_BOT_TOKEN`
- `SLACK_SIGNING_SECRET`
- `ANTHROPIC_API_KEY`

**Optional, with defaults:**

- `CORTEX_BIND_ADDR` full socket override
- `CORTEX_HOST` host part when `CORTEX_BIND_ADDR` is not set
- `CORTEX_PORT` port part when `PORT` is not set (default `23001`)
- `PORT` Cloud Run-provided port; defaults the host to `0.0.0.0`
- `CORTEX_DEFAULT_WAIT_TIMEOUT_SECONDS` default `WaitForResponse` timeout (default `300`)
- `CORTEX_LOG_LEVEL` `trace` | `debug` | `info` (default) | `warn` | `error`
- `SLACK_CHANNEL_PREFIX` (default `agent-`)
- `SLACK_API_BASE_URL` (default `https://slack.com/api`)
- `CORTEX_CLAUDE_MODEL` (default `claude-sonnet-4-6`)

**Slack manifest auto-registration** (opt-in) — when enabled, the server pushes
`integrations/slack/manifest.yaml` to Slack on startup (after the listener is up,
so Slack's request-URL challenge can be answered), pointing the app's request
URLs and event subscriptions at this instance. This is handled by
`internal/slackadmin` and runs from `cmd/cortex/main.go`. It is non-fatal: a
failure is logged and the server keeps serving.

- `CORTEX_SLACK_AUTOREGISTER` set truthy (`1`/`true`/`yes`/`on`) to enable; off by default
- `SLACK_APP_ID` required when enabled (e.g. `A0123456789`)
- `SLACK_CALLBACK_URL` required when enabled — public HTTPS base, no trailing slash (e.g. your reserved ngrok domain or the Cloud Run URL)
- `SLACK_CONFIG_REFRESH_TOKEN` first-run seed only; an app-config refresh token from api.slack.com. After the first run, tokens are read from the state file and rotated automatically (`tooling.tokens.rotate`)
- `CORTEX_SLACK_TOKENS_PATH` state file for the rotating config token (default `$XDG_CONFIG_HOME/cortex/slack-tokens.json`, mode `0600`)
- `CORTEX_SLACK_MANIFEST_PATH` manifest to push (default `integrations/slack/manifest.yaml`)

The `make slack-sync` target remains as a manual, out-of-band alternative.

## Infrastructure

Terraform configuration is located in the `terraform/` directory with GCP-focused infrastructure:

- Organized with separate files for versions, backend, providers, variables, locals, data sources, main composition, and outputs
- Supports multiple environments via `environments/*.tfvars` files
- Uses remote state backend (GCS) configured via `backend.hcl`
- Run terraform commands from the `terraform/` directory

```bash
# Initialize with backend config
terraform init -backend-config=backend.hcl

# Plan changes for specific environment
terraform plan -var-file=environments/dev.tfvars

# Apply changes
terraform apply -var-file=environments/dev.tfvars
```

The current scaffold targets:

- Cloud Run for the Cortex service
- Artifact Registry for container images
- Secret Manager for Slack credentials
- IAM service accounts and public invoker access for the MVP

## Development Environment

The following tools must be on PATH. Installation method is the developer's choice (Homebrew, asdf, mise, manual, etc.) — the project does not mandate one:

- Go (latest stable)
- `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` — only for `make proto`; generated bindings are checked in
- `terraform` — only for the infrastructure workflow
- Google Cloud SDK — for authentication and deployment workflows

Once Go is installed, `make build` / `make run` / `make test` work without the other tools.

## Code Modification Guidelines

When modifying the gRPC service:
1. Update `proto/cortex.proto` for any service or message changes
2. Run `make proto` to regenerate `internal/cortexpb/`
3. Update the service implementation in `internal/server/` and any affected helper packages
4. Run `make ci` before finishing a change
