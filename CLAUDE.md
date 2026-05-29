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
CORTEX_HOST=127.0.0.1 CORTEX_PORT=50051 make run

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

# Open a public cloudflared tunnel for Slack callbacks
make tunnel TUNNEL_PORT=50051
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

`proto/cortex.proto` is the source of truth. `make proto` regenerates `internal/cortexpb/cortex.pb.go` and `internal/cortexpb/cortex_grpc.pb.go`. The generated files are checked in. Requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on PATH (all provided by `mise install`).

## Architecture

### gRPC Service Layer

The core service is defined in `proto/cortex.proto` and implements `CortexAgentService` with three RPC methods:

1. **SendEvent**: receives `AgentSignal` messages from agents containing agent name, session ID, repository, and message content
2. **WaitForResponse**: blocking call that waits for a human response based on session ID, with a bounded timeout
3. **SubmitHumanResponse**: records a human response for an existing session

The service is implemented in `internal/server/grpc.go` using an in-memory `sessions.Manager` (see `internal/sessions/`). Agent events are posted into Slack as normal messages; human replies arrive through Slack Events API message callbacks handled at `POST /slack/events` in `internal/server/http.go`.

`cmd/cortex/main.go` runs gRPC and HTTP on the same TCP listener via `h2c` — application/grpc requests go to the gRPC server, everything else hits the HTTP mux. This keeps Cloud Run single-port deployment trivial.

### Slack Identity Model

One Slack app, one channel per agent (`#<prefix><agent>`). On the first `SendEvent` for a given agent:

1. `slack.App.ensureChannel` checks the per-agent cache, then calls `conversations.create`. If `name_taken`, it falls back to `conversations.list` and `conversations.join`.
2. The bot posts `chat.postMessage` with `username` set to the agent name so each agent presents a distinct identity in Slack without a separate Slack app per agent.
3. The resulting `(channel_id, thread_ts)` is stored against the session, and inbound `message` events route by that pair.

### Server Configuration

The bind address is configurable:

- `CORTEX_BIND_ADDR` overrides the full socket address
- `CORTEX_HOST` overrides the host when `CORTEX_BIND_ADDR` is not set
- `CORTEX_PORT` overrides the port when `PORT` is not set
- `PORT` is used automatically for Cloud Run and defaults the host to `0.0.0.0`

Slack configuration is driven by:

- `SLACK_BOT_TOKEN`
- `SLACK_SIGNING_SECRET`
- `SLACK_CHANNEL_PREFIX` (optional, default `agent-`)
- `SLACK_API_BASE_URL` (optional, default `https://slack.com/api`)

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

Tool management is handled by `mise` (configured in `mise.toml`):
- Go (latest stable)
- `protoc`
- `protoc-gen-go`, `protoc-gen-go-grpc` (installed as `go:` tools)
- `terraform`

The Google Cloud SDK is also required locally for authentication and deployment workflows.

Run `mise install` to set up the development environment.

## Code Modification Guidelines

When modifying the gRPC service:
1. Update `proto/cortex.proto` for any service or message changes
2. Run `make proto` to regenerate `internal/cortexpb/`
3. Update the service implementation in `internal/server/` and any affected helper packages
4. Run `make ci` before finishing a change
