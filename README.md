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
CORTEX_PORT=23001 \
SLACK_BOT_TOKEN=xoxb-... \
SLACK_SIGNING_SECRET=signing-secret \
make run

# Open a public ngrok tunnel to your local server for Slack callbacks
make tunnel TUNNEL_PORT=23001

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

`make proto` regenerates Go bindings from `proto/cortex.proto` into `internal/cortexpb/`. It expects `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` to be on PATH (see [Development Environment](#development-environment)).

## Architecture

### Startup & Execution Flow

Everything is orchestrated from `cmd/cortex/main.go`. A boot proceeds in four phases:

**1. Boot / fail-fast.** The server validates its environment and external dependencies before it accepts any traffic, so misconfiguration fails the process rather than the first request.

1. `config.FromEnv()` parses and validates every environment variable. A missing required var (`SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, `ANTHROPIC_API_KEY`) or an invalid value writes to stderr and exits non-zero. This is also where the optional Slack auto-registration config is assembled — `cfg.Register` is non-nil only when `CORTEX_SLACK_AUTOREGISTER` is enabled.
2. The structured logger is built from `CORTEX_LOG_LEVEL` and installed as the slog default.
3. The Slack client is created and `auth.test` is called (10s timeout). A bad or revoked bot token exits non-zero; on success the team and bot user are logged (`slack auth verified`).

**2. Wiring.** With config trusted, the in-process components are constructed: the in-memory `sessions.Manager`, the `claude.Thinker`, the `agents.Manager`, and the `/cortex` slash-command handler. The gRPC service and the HTTP mux (`/healthz`, `/slack/events`, `/slack/commands`) are assembled, the HTTP side wrapped in the request-logging middleware. A single `mixed` handler dispatches `application/grpc` HTTP/2 requests to the gRPC server and everything else to the HTTP mux, all served over `h2c` so both protocols share **one plaintext port** (required by Cloud Run's single-port model).

**3. Serve and (optionally) self-register.** Signal handling for `SIGINT`/`SIGTERM` is armed, then:

1. `srv.ListenAndServe()` runs in a goroutine; a fatal serve error is delivered on an internal channel.
2. If `cfg.Register != nil`, a second goroutine runs `registerSlack`: it first polls the local `/healthz` until it returns `200` (`waitForReady`), and only then calls `slackadmin.Register` to push the manifest. The ordering matters — pushing `event_subscriptions` makes Slack immediately POST a signed challenge to `/slack/events`, which only succeeds if the listener is already up. Registration failures are logged but never fatal: the server keeps serving so you can fix credentials or the callback URL and restart. See [automatic registration](integrations/slack/slack.md#option-a-automatic-registration-recommended) in `slack.md`.

**4. Steady state and shutdown.** The main goroutine blocks until either a termination signal arrives or the serve goroutine reports a fatal error (which exits non-zero). On signal, shutdown is graceful with a 10s deadline: the gRPC server is stopped, the agent manager is shut down, and the HTTP server is drained.

```
config.FromEnv ──▶ logger ──▶ slack auth.test ──▶ wire components ──▶ build h2c handler
                                   │ (fail-fast exits)                        │
                                   ▼                                          ▼
                          exit 1 on bad token                     ListenAndServe (goroutine)
                                                                              │
                                              cfg.Register != nil ?           │
                                                      │ yes                   │
                                                      ▼                       │
                              waitForReady(/healthz) ──▶ slackadmin.Register  │
                                                                              ▼
                                                          block on signal | serve error
                                                                              │
                                                                              ▼
                                                      graceful stop (gRPC, agents, HTTP)
```

### gRPC Service Layer

The core service is defined in `proto/cortex.proto` and currently exposes:

1. `SendEvent`, which receives agent messages and session metadata
2. `WaitForResponse`, which blocks for a bounded time and returns `RESPONDED`, `TIMED_OUT`, or `NOT_FOUND`
3. `SubmitHumanResponse`, which records a human response for a session

The server multiplexes gRPC (`application/grpc` on HTTP/2) and plain HTTP on a single h2c listener — Cloud Run only exposes one port and this keeps deployment trivial.

The current prototype keeps session state in memory. `SendEvent` resolves the agent name to a dedicated Slack channel (creating `#<prefix><agent>` on first use, looking it up if it already exists), posts the request as a normal message there, stores the resulting `(channel_id, thread_ts)` against the session, and waits for a human reply in that thread. `WaitForResponse` blocks until the response arrives or the timeout expires. The Slack HTTP endpoint verifies request signatures before accepting event callbacks.

The end-to-end Slack message flow (agent → channel → human reply → unblocked session) is documented in [`integrations/slack/slack.md`](integrations/slack/slack.md#how-it-works-runtime-flow).

### Slash Command Flow

`/cortex` slash commands take a separate inbound path from agent gRPC traffic and from Slack events. They are handled synchronously by `CommandsHandler` (`internal/server/commands.go`):

1. **Slack → server.** Slack POSTs the command to `<SLACK_CALLBACK_URL>/slack/commands` (the URL in the manifest's `slash_commands` block) as `application/x-www-form-urlencoded` — unlike events, which are JSON.
2. **Routing.** The `mixed` handler in `main.go` sees a non-gRPC request and forwards to the HTTP mux, which routes `/slack/commands` to `CommandsHandler.ServeHTTP`.
3. **Verify + parse.** The handler reads the body, verifies the Slack signature (same gate as events; bad signature → `401`), then `url.ParseQuery`s the form into a `slashCommand` (`command`, `text`, `user_id`, `channel_id`, `response_url`).
4. **Dispatch.** `dispatch` tokenizes the text and routes `agent create|destroy|list` (and `help`) to the matching handler:
   - `create`: validate the name → `Agents.Create` (provision the channel + spawn the agent goroutine) → post an async `:wave:` welcome into the new channel → reply.
   - `destroy`: `Agents.Destroy` (stop the goroutine + archive the channel) → reply.
   - `list`: `Agents.List` → reply.
5. **Reply.** The handler returns a JSON `slashResponse` **synchronously** in the HTTP response, with a `response_type` of either `ephemeral` (only the invoking user sees it — errors, `list`, usage) or `in_channel` (visible to everyone — successful create/destroy).

```
/cortex agent create demo-bot
        │
        ▼  POST /slack/commands  (form-encoded)
mixed handler ──▶ HTTP mux ──▶ CommandsHandler.ServeHTTP
        │
        ├─ verify signature (401 on failure)
        ├─ parse form → slashCommand
        └─ dispatch ──▶ handleCreate ──▶ Agents.Create (channel + goroutine)
                                  │                └─ async: post welcome to channel
                                  ▼
                        JSON reply (ephemeral | in_channel) ──▶ Slack renders to user
```

Slack requires a response within ~3 seconds. Create/destroy are fast enough (one channel API call plus a goroutine spawn) to run inline, so the command replies directly and the `response_url` deferred-response two-step is deliberately unused. If an operation ever became slow, the handler would ack immediately and post the result to `response_url` instead. This contrasts with the event path, which returns `200` immediately and does its work asynchronously.

### Agent Runtime

An "agent" has no separate process — it is a single **goroutine plus an in-memory mailbox**, managed by `agents.Manager` (`internal/agents/`). The runtime is always in-memory; whether that state *survives a restart* depends on the configured `Store` (see [Persistence](#agent-persistence) below).

**Creation** (`Manager.Create`): provision the Slack channel (`EnsureChannelForAgent` creates or finds `#<prefix><name>`), then construct the `Agent` and start its `run()` goroutine. The agent is indexed by name (for destroy/list) and by channel ID (for message routing). It is created with a background context, not the request context, so it outlives the slash command and runs until destroyed or the server shuts down.

**The run loop** (`agent.go`) selects over two things: a cancellation signal (stop) and an `inbox` channel (a buffered queue of incoming messages, capacity 32; a full inbox drops messages with a warning). Messages are handled one at a time, so an agent's turns never race.

**Who it talks to.** Each agent sits between one inbound source and two outbound services:

1. **Inbound — Slack events.** A top-level message in the agent's channel arrives at `/slack/events`, and `handleMessage` → `Manager.RouteMessage` → `agent.deliver` puts it on the `inbox`.
2. **Outbound — Anthropic (to think).** `handle` snapshots the history, sets a 60s timeout, and calls `Thinker.Respond`, which POSTs to the Anthropic Messages API (`internal/claude/`) with a per-agent system prompt, the conversation history, and the new message. Model is `CORTEX_CLAUDE_MODEL` (default `claude-sonnet-4-6`).
3. **Outbound — Slack (to reply).** The returned text is appended to history (bounded to the last 50 turns) and posted back into the channel via `PostToChannel` (`chat.postMessage`). A thinker error posts an `_(agent error: …)_` notice instead.

```
human msg in #agent-demo-bot
        │  POST /slack/events
        ▼
handleMessage ──▶ Manager.RouteMessage ──▶ agent.deliver ──▶ inbox
                                                               │
                                            run() loop ──▶ handle()
                                                               │
                                  Thinker.Respond ──▶ POST Anthropic Messages API
                                                               │
                              reply ──▶ PostToChannel ──▶ Slack chat.postMessage ──▶ channel
```

A single `Thinker` is shared across all agents — it holds the Anthropic client and is stateless per-agent (the only per-agent inputs are the name and the history the agent passes in). The `agents.Thinker` interface is the seam that decouples the agent goroutine from Anthropic, so tests can substitute a fake (and `EchoThinker` serves as a placeholder that just echoes).

#### Agent persistence

Agent state — name, channel ID, and conversation history — can be mirrored to a `Store` so agents survive restarts. The runtime is unchanged: agents still run from the in-memory maps; the `Store` only decides whether that state is *also* written durably and *reloaded on boot*.

- `Manager.Create` saves the new agent; each completed turn re-saves its (bounded) history; `Manager.Destroy` deletes it.
- On startup, `Manager.Restore` reloads persisted agents and respawns their goroutines from the stored channel ID — **without** re-provisioning Slack channels.

**Choosing a backend.** The only knob is the `CORTEX_STATE_DIR` environment variable:

| Backend | How to select it | Behavior |
|---|---|---|
| `NoopStore` | leave `CORTEX_STATE_DIR` **unset** (default) | persistence off — agents are forgotten on restart. *Not* an in-memory store, just no durable writes. |
| `FileStore` | set `CORTEX_STATE_DIR=<dir>` | one atomic JSON file per agent under `<dir>`; agents reload on boot. Fixes local and warm in-instance restarts. |
| `MemoryStore` | not selectable at runtime | test-only (`agents.NewMemoryStore`); does not survive restarts. |

```bash
# Persistence off (default) — nothing to set:
make run

# Persistence on — agents survive a restart:
CORTEX_STATE_DIR=./.cortex-state make run
```

With `CORTEX_STATE_DIR` set you'll see `restored agents count=N` in the logs on boot, and a `<agent>.json` file appear under the directory for each agent. Point it at a path that persists across restarts (and, on a VM, across deploys). The directory is created if missing.

> **Cloud Run caveat:** `FileStore` does **not** survive scale-to-zero — each new container gets a fresh ephemeral disk. Truly covering Cloud Run needs a network-backed `Store` (GCS/Firestore), which the `Store` interface makes a drop-in addition. That is deliberately a follow-up; sessions (`sessions.Manager`) remain in-memory by design.

### Runtime Configuration

**Required.** The server refuses to start if any of these are missing or invalid. `SLACK_BOT_TOKEN` is verified via Slack's `auth.test` at boot, so a bad/revoked token fails fast rather than at first use.

- `SLACK_BOT_TOKEN`: bot token (`xoxb-...`); see [slack.md](integrations/slack/slack.md#1-create-the-slack-app) for the required scopes
- `SLACK_SIGNING_SECRET`: signing secret used to verify Slack event and slash command requests
- `ANTHROPIC_API_KEY`: Anthropic API key — each agent goroutine routes incoming Slack messages through the Anthropic Messages API

**Optional, with defaults.**

- `CORTEX_BIND_ADDR`: full socket address override such as `0.0.0.0:8080`
- `CORTEX_HOST`: host override when `CORTEX_BIND_ADDR` is not set (default `127.0.0.1`, or `0.0.0.0` if `PORT` is set)
- `CORTEX_PORT`: port override when `PORT` is not set (default `23001`)
- `PORT`: Cloud Run-provided port; when present, Cortex defaults to `0.0.0.0:$PORT`
- `CORTEX_DEFAULT_WAIT_TIMEOUT_SECONDS`: default `WaitForResponse` timeout when the request omits one (default `300`)
- `CORTEX_STATE_DIR`: directory for persisting agent state so agents survive restarts (default empty = persistence off). See [Agent persistence](#agent-persistence)
- `CORTEX_CLAUDE_MODEL`: model id (default `claude-sonnet-4-6`)
- `CORTEX_LOG_LEVEL`: one of `trace`, `debug`, `info` (default), `warn`, `error`. At `debug`, every HTTP request and gRPC RPC is logged with method, path, status, and duration. At `trace`, the full request body and headers are logged too (the `X-Slack-Signature` header is auto-redacted).

**Slack-specific configuration** — the bot credentials above plus channel-prefix, API-base, and the manifest auto-registration variables (`CORTEX_SLACK_AUTOREGISTER`, `SLACK_APP_ID`, `SLACK_CALLBACK_URL`, `SLACK_CONFIG_REFRESH_TOKEN`, …) are documented in [`integrations/slack/slack.md`](integrations/slack/slack.md#slack-related-environment-variables). That guide also covers self-registration of the app manifest on startup; see [`AUTOMATION.md`](integrations/slack/AUTOMATION.md) for the rotation internals.

### Running with TRACE logging

For debugging Slack callbacks or watching agent traffic in real time, run the server with `CORTEX_LOG_LEVEL=trace`:

```bash
CORTEX_LOG_LEVEL=trace \
SLACK_BOT_TOKEN=xoxb-... \
SLACK_SIGNING_SECRET=... \
ANTHROPIC_API_KEY=sk-ant-... \
make run
```

You'll see two log lines per incoming request — a `DEBUG` summary and a `TRACE` line with the full body. Example output for one Slack event delivery:

```
level=DEBUG msg=http.request method=POST path=/slack/events remote=10.0.0.1:12345 status=200 duration=412.3µs
level=TRACE msg=http.request.trace method=POST path=/slack/events ... body="{\"type\":\"event_callback\",...}"
```

For gRPC, the equivalent lines are `grpc.request` and `grpc.request.trace`, with the parsed request proto on the trace line.

TRACE is intentionally noisy and writes full request bodies to stderr — keep it off in production.

### Project Layout

- `proto/`: gRPC service and message contracts
- `internal/cortexpb/`: generated Go bindings
- `internal/config/`: env-driven runtime configuration
- `internal/sessions/`: in-memory session and Slack-thread bookkeeping
- `internal/slack/`: Slack client built on [`github.com/slack-go/slack`](https://github.com/slack-go/slack) (channel ensure, post, signature verify, event parsing)
- `internal/slackadmin/`: Slack app-config token rotation and manifest auto-registration (`apps.manifest.update`, `tooling.tokens.rotate`)
- `internal/agents/`: per-agent goroutines that turn Slack channel messages into Claude turns
- `internal/claude/`: Anthropic Messages API client (`Thinker`)
- `internal/server/`: gRPC service implementation, HTTP handlers, and request-logging middleware
- `internal/logging/`: structured slog handler and log-level parsing
- `cmd/cortex/`: program entry point and startup orchestration
- `terraform/`: GCP infrastructure scaffold for Cloud Run deployment
- `Makefile`: local development and CI entrypoints
- `integrations/slack/`: Slack app manifest and setup/automation docs

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

Building and running Cortex requires the following tools on your PATH. How you install them is up to you (Homebrew, asdf, mise, manual download, etc.):

- Go (latest stable)
- `protoc` — only needed to regenerate protobuf bindings (`make proto`); the generated files are checked in
- `protoc-gen-go` and `protoc-gen-go-grpc` — likewise, only for `make proto`
- `terraform` — only for the infrastructure workflow below
- Google Cloud SDK (`gcloud`) — for authentication and deployment workflows

Once Go is installed, `make build` / `make run` / `make test` work on their own; the protobuf and Terraform tools are only needed for those specific workflows. After installing the tools, authenticate with GCP before using Terraform.

For end-to-end Slack setup — app manifest, OAuth scopes, Event Subscriptions, ngrok tunnel, and troubleshooting — see [`integrations/slack/slack.md`](integrations/slack/slack.md).

## Code Modification Guidelines

When modifying the gRPC service:

1. Update `proto/cortex.proto` for service or message changes
2. Run `make proto` to regenerate `internal/cortexpb/`
3. Update the implementation in `internal/server/` and any affected packages
4. Run `make ci` before finishing a change
