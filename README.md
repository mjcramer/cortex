# cortex

## Project Overview

Cortex is an AI agent manager hub that communicates via messaging apps. The current MVP target is a single Go service that accepts agent events over gRPC, routes them to a human over Slack, and returns the human response back to the waiting agent.

### MVP Scope

- One Go service that manages agent sessions
- One human messaging adapter, starting with Slack
- One Slack channel per agent (`#<prefix><agent>`), auto-created on first event; replies route by channel + thread
- One deploy target on Google Cloud Run
- One Terraform stack in `terraform/` for the runtime infrastructure

### Non-Goals

- Multiple messaging platforms in the first release
- Durable persistence beyond in-memory session state
- Multi-region or horizontally scaled deployments
- A separate admin UI

## Build System

This project uses Make over `go` for build orchestration. Protobuf bindings are checked in but can be regenerated.

### Common Commands

```bash
# Build the binary into ./bin/cortex
make build

# Build a stripped release binary
make release

# Run the server locally (gRPC + HTTP on the same port via h2c)
make run

# Run the server with explicit environment overrides
CORTEX_HOST=127.0.0.1 \
CORTEX_PORT=50051 \
SLACK_BOT_TOKEN=xoxb-... \
SLACK_SIGNING_SECRET=signing-secret \
make run

# Open a public ngrok tunnel to your local server for Slack callbacks
make tunnel TUNNEL_PORT=50051

# Run tests
make test

# Run the local CI checks
make ci

# Format Go sources
make fmt

# Check formatting without modifying files
make fmt-check

# Regenerate protobuf bindings
make proto
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

`make proto` regenerates Go bindings from `proto/cortex.proto` into `internal/cortexpb/`. The build expects `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` to be on PATH; `mise install` provides all three.

## Architecture

### gRPC Service Layer

The core service is defined in `proto/cortex.proto` and currently exposes:

1. `SendEvent`, which receives agent messages and session metadata
2. `WaitForResponse`, which blocks for a bounded time and returns `RESPONDED`, `TIMED_OUT`, or `NOT_FOUND`
3. `SubmitHumanResponse`, which records a human response for a session

The server multiplexes gRPC (`application/grpc` on HTTP/2) and plain HTTP on a single h2c listener — Cloud Run only exposes one port and this keeps deployment trivial.

The current prototype keeps session state in memory. `SendEvent` resolves the agent name to a dedicated Slack channel (creating `#<prefix><agent>` on first use, looking it up if it already exists), posts the request as a normal message there, stores the resulting `(channel_id, thread_ts)` against the session, and waits for a human reply in that thread. `WaitForResponse` blocks until the response arrives or the timeout expires. The Slack HTTP endpoint verifies request signatures before accepting event callbacks.

### Slack Prototype Flow

1. An agent calls `SendEvent` with an `agent`, `session_id`, `repo`, and `message`.
2. Cortex finds or creates `#<SLACK_CHANNEL_PREFIX><agent>` and posts the request there, using the agent name as the bot display name.
3. A human replies in the Slack thread like they would to any other teammate.
4. Slack sends the message event payload to `POST /slack/events`.
5. Cortex verifies the Slack signature, matches `(channel_id, thread_ts)` back to the session, records the response, and unblocks `WaitForResponse`.

### Runtime Configuration

The server supports the following bind configuration:

- `CORTEX_BIND_ADDR`: full socket address override such as `0.0.0.0:8080`
- `CORTEX_HOST`: host override when `CORTEX_BIND_ADDR` is not set
- `CORTEX_PORT`: port override when `PORT` is not set
- `PORT`: Cloud Run-provided port; when present, Cortex defaults to `0.0.0.0:$PORT`
- `CORTEX_DEFAULT_WAIT_TIMEOUT_SECONDS`: default `WaitForResponse` timeout when the request does not provide one

Logging is configured with:

- `CORTEX_LOG_FORMAT`: `console`/`text` for the colorized human-readable console layout, or `json` for structured logs. Defaults to `json` when `PORT` is set (Cloud Run) and `console` locally.
- `CORTEX_LOG_LEVEL`: `trace`, `debug`, `info`, `warn`, or `error` (default `info`). `trace` is an alias for `debug`. Set `debug` to log agent and Slack message bodies.
- `CORTEX_LOG_COLOR`: `always` or `never` to force ANSI colors; otherwise color is enabled when stderr is a TTY and `NO_COLOR` is unset.

The Slack prototype uses:

- `SLACK_BOT_TOKEN`: bot token with `chat:write`, `chat:write.customize`, `channels:manage`, `channels:read`, and `channels:join`
- `SLACK_SIGNING_SECRET`: signing secret used to verify Slack event requests
- `SLACK_CHANNEL_PREFIX`: prefix prepended to sanitized agent names when forming channel names (default `agent-`)
- `SLACK_API_BASE_URL`: optional Slack API base URL override, primarily useful for tests

### Project Layout

- `proto/`: gRPC service and message contracts
- `internal/cortexpb/`: generated Go bindings
- `internal/config/`: env-driven runtime configuration
- `internal/sessions/`: in-memory session and Slack-thread bookkeeping
- `internal/slack/`: Slack client (channel ensure, post, signature verify, event types)
- `internal/server/`: gRPC service implementation and HTTP handler
- `cmd/cortex/`: program entry point
- `terraform/`: GCP infrastructure scaffold for Cloud Run deployment
- `Makefile`: local development and CI entrypoints
- `mise.toml`: local toolchain definition

## Infrastructure

Terraform configuration lives in `terraform/` and is GCP-focused:

- `versions.tf`: Terraform and Google provider version constraints
- `backend.tf`: remote state backend declaration for GCS
- `backend.hcl.example`: example backend settings for the state bucket
- `providers.tf`: Google provider configuration
- `variables.tf`: input variables for project, region, image, and scaling
- `locals.tf`: shared naming and label conventions
- `data.tf`: project metadata lookups
- `main.tf`: Cloud Run, Artifact Registry, Secret Manager, IAM, and API enablement
- `outputs.tf`: deployment outputs such as service URL and repository path
- `environments/*.tfvars`: per-environment values

The initial GCP MVP infrastructure targets:

- Cloud Run for the Cortex service
- Artifact Registry for container images
- Secret Manager for Slack credentials
- IAM service accounts and invoker bindings

Typical workflow:

```bash
# Authenticate locally first
gcloud auth application-default login

# Initialize Terraform with a GCS backend
terraform -chdir=terraform init -backend-config=backend.hcl

# Review the dev environment plan
terraform -chdir=terraform plan -var-file=environments/dev.tfvars

# Apply the selected environment
terraform -chdir=terraform apply -var-file=environments/dev.tfvars
```

## Development Environment

Tool management is handled by `mise` in `mise.toml`:

- Go (latest stable)
- `protoc`
- `protoc-gen-go` and `protoc-gen-go-grpc`
- `terraform`

You also need the Google Cloud SDK available locally for authentication and deployment workflows.

Run `mise install` to set up the local toolchain, then authenticate with GCP before using Terraform.

To exercise the Slack prototype locally:

1. Create a Slack app with `chat:write`, `chat:write.customize`, `channels:manage`, `channels:read`, and `channels:join`
2. Enable Event Subscriptions and point the request URL at `https://<your-host>/slack/events`
3. Subscribe to `message.channels` for public channels
4. Install the app in a workspace where it can create and join the `#<prefix><agent>` channels
5. Export `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` in your shell
6. Start the server and `make tunnel` to expose `/slack/events` to Slack

## Code Modification Guidelines

When modifying the gRPC service:

1. Update `proto/cortex.proto` for service or message changes
2. Run `make proto` to regenerate `internal/cortexpb/`
3. Update the implementation in `internal/server/` and any affected packages
4. Run `make ci` before finishing a change
