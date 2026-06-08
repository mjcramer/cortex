# Design: Disposable Claude Code Agent Environments

Status: **Draft** · Milestone: M4 (Agent Runtime) · Tracks issue #15 (design) for #1, #16, #17

## 1. Problem & motivation

Today a Cortex "agent" is a chat persona. Each turn runs through
[`internal/claude/thinker.go`](../internal/claude/thinker.go), which calls the
Anthropic **Messages API** once and returns text. The agent has no filesystem,
no repo, and no tools — it can talk *about* code but cannot build, run, or
change it.

Issue #1 asks for the opposite capability: give an agent a real, **disposable
environment** with the target repo checked out and a full build/compile/debug
toolchain, plus **Claude Code** itself installed, so the agent can do work
(edit, build, test, open a PR) and then have the environment destroyed and its
resources released.

This doc covers how such an environment is provisioned, its lifecycle, where it
runs, the isolation boundary, and how it ties back into the existing Cortex
session/Slack flow. It ends with the follow-up implementation issues.

## 2. Goals / non-goals

**Goals**
- A reproducible container image that bundles Claude Code + a build/debug
  toolchain and can clone a repo and run `make`/tests inside it (issue #16).
- An orchestrator that can **provision → run a task → collect output → tear
  down** an environment on demand, with *guaranteed* teardown on success,
  failure, or timeout (issue #17).
- A clean Go interface so the first backend (local Docker) can be swapped for a
  cloud backend (Cloud Run Jobs) without touching call sites.
- Integration with the existing session model so a long-running task can still
  ask a human for input over Slack via `WaitForResponse`.

**Non-goals (for this milestone)**
- Multi-tenant hardening / running untrusted third-party repos. We assume the
  operator owns the repos being worked on.
- Horizontal autoscaling, queueing, or fair-share scheduling across many
  concurrent tasks. One task → one environment is enough for the prototype.
- Replacing the chat `Thinker`. The chat persona stays; the runtime is a
  second, heavier execution path selected per task.

## 3. Where it runs

**Decision: start with a local Docker backend, behind a `Runner` interface, and
add a Cloud Run Jobs backend later.**

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Local Docker** | Fastest to a working prototype; trivial to debug; no cloud creds needed for dev | Not how prod will run; bound to one host's resources | **Phase 1** |
| **Cloud Run Jobs** | No always-on infra, billed per execution, fits existing GCP/Artifact Registry/Terraform scaffold | Cold start; exec streaming and cancellation are more work; 1 task = 1 Job execution | **Phase 2** |
| GCE / GKE | Most control over isolation and resources | Heaviest to build and operate; always-on cost | Deferred |

The point of the `Runner` interface (below) is that this decision is reversible:
`DockerRunner` and `CloudRunJobsRunner` are two implementations of the same
contract, chosen by config.

## 4. Architecture

### 4.1 The `Runner` interface

A new package `internal/runtime` owns the lifecycle. The orchestrator depends on
an interface, not Docker:

```go
package runtime

// TaskSpec is everything needed to run one disposable task.
type TaskSpec struct {
    SessionID string   // ties the run back to a Cortex session (Slack thread)
    Agent     string   // which agent/persona requested it
    Repo      string   // git URL or "owner/name"
    Ref       string   // branch/commit to check out (default: repo default branch)
    Prompt    string   // the task instruction handed to Claude Code
    Env       []string // extra KEY=VALUE passed into the container
    Timeout   time.Duration
}

// Result is collected after the task finishes (or is killed).
type Result struct {
    ExitCode int
    Output   string        // captured Claude Code transcript / stdout tail
    Artifacts []Artifact   // e.g. a diff, a branch name, a PR URL
    Duration time.Duration
    TimedOut bool
}

// Runner provisions an environment, runs the task, collects output, and
// guarantees teardown before Run returns.
type Runner interface {
    Run(ctx context.Context, spec TaskSpec) (*Result, error)
}
```

`Run` is synchronous from the caller's perspective and **owns teardown**: by the
time it returns (value *or* error *or* ctx cancellation), the environment is
gone. This makes the "no resources leak" acceptance criterion a property of the
interface, not of each call site.

### 4.2 Phase-1 backend: `DockerRunner`

```
DockerRunner.Run(ctx, spec):
  1. create a per-task workspace volume / temp dir
  2. docker run --rm \
       -e ANTHROPIC_API_KEY -e CORTEX_SESSION_ID=spec.SessionID ... \
       --network <restricted> \
       cortex-agent-runtime:<pinned> \
       /usr/local/bin/agent-entrypoint
       (entrypoint clones spec.Repo@Ref, then runs Claude Code headless)
  3. stream container logs → Result.Output (and optionally → Slack thread)
  4. on ctx.Done()/timeout: docker kill; mark Result.TimedOut
  5. defer: docker rm -f + volume rm   (teardown is unconditional)
```

`--rm` plus an explicit `docker rm -f`/volume cleanup in a `defer` gives
belt-and-suspenders teardown. Timeout is enforced both by the Go `context`
deadline and by `spec.Timeout` inside the container entrypoint.

### 4.3 The runtime image (issue #16)

`build/agent-runtime/Dockerfile` produces `cortex-agent-runtime`:

- Base: a pinned Node.js image (Claude Code is distributed as the npm package
  `@anthropic-ai/claude-code` and needs Node).
- Installs Claude Code at a **pinned version**.
- Installs the build/debug toolchain the target repos need. For *this* repo:
  Go (pinned), `git`, `make`, `protoc` + plugins, plus common debug tools. The
  toolchain list is intentionally per-target and lives in the image, not hard
  coded in the orchestrator.
- `agent-entrypoint` script: `git clone` the repo at the requested ref, `cd`
  in, then invoke Claude Code headless and capture its transcript.
- Reproducible & pinned (digest-pinned base, locked tool versions), published to
  **Artifact Registry** alongside the cortex service image.

Claude Code runs **non-interactively** (headless / "print" mode) so the
entrypoint can pass the prompt in and capture a structured transcript out,
rather than driving a TTY. The exact invocation and output format are settled in
#16 against the pinned CLI version.

### 4.4 Secrets

The container needs `ANTHROPIC_API_KEY` and, to push branches/PRs, a scoped git
token. These are injected as env at `docker run` time (phase 1) and mounted from
**Secret Manager** in the cloud backend (phase 2), consistent with how Slack
creds are handled (issue #11). Secrets are never baked into the image.

## 5. Lifecycle

```
                    SendEvent / slash command
                              │
                              ▼
                    ┌──────────────────┐
   provision ──────▶│  TaskSpec built  │
                    └──────────────────┘
                              │ Runner.Run(ctx, spec)
                              ▼
        ┌─────────────────────────────────────────────┐
        │  container: clone repo → run Claude Code      │
        │  ▲ may call back via WaitForResponse for      │
        │  │ human input over the Slack thread          │
        └─────────────────────────────────────────────┘
                              │ logs stream → Slack thread
                              ▼
                    ┌──────────────────┐
   collect ────────▶│  Result captured │  (diff, branch, PR URL, exit code)
                    └──────────────────┘
                              │
                              ▼
   teardown ───────▶  docker kill/rm + volume rm   (always, via defer)
                              │
                              ▼
                    post summary to Slack thread
```

Teardown triggers on **all** exits: normal completion, non-zero exit, error,
context cancellation (shutdown), and timeout.

## 6. Integration with Cortex

The runtime slots into the existing flow rather than replacing it:

- **Trigger.** A task starts from an inbound signal that already carries a
  `repo` field — see `AgentSignal.repo` in
  [`proto/cortex.proto`](../proto/cortex.proto) — or from a Slack slash command
  (`internal/server/commands.go`). The handler builds a `TaskSpec` and calls
  `Runner.Run`.
- **Session reuse.** `TaskSpec.SessionID` is the existing Cortex session id, so
  a running task can block on human input through the current
  `WaitForResponse` / `SubmitHumanResponse` RPCs and the Slack thread bound to
  that session. No new human-in-the-loop machinery is needed.
- **Wiring.** `cmd/cortex/main.go` constructs a `Runner` from config (Docker vs
  Cloud Run Jobs) and hands it to the server/command handlers, the same way it
  constructs the `Thinker`, `sessions.Manager`, and agent `Manager` today.
- **Output.** The container's transcript streams into the agent's Slack thread
  (reusing the `Replier` boundary), and a final summary — branch name, diff
  stat, PR URL, pass/fail — is posted when `Run` returns.

### Proposed config (env), following existing conventions

| Var | Meaning | Default |
|---|---|---|
| `CORTEX_RUNTIME_BACKEND` | `off` \| `docker` \| `cloudrun` | `off` |
| `CORTEX_RUNTIME_IMAGE` | runtime image ref (digest-pinned) | required when on |
| `CORTEX_RUNTIME_TASK_TIMEOUT_SECONDS` | hard cap per task | `1800` |
| `CORTEX_RUNTIME_GIT_TOKEN` | scoped token for clone/push (Secret Manager in prod) | — |

`off` keeps current behavior exactly; the runtime is strictly additive.

## 7. Isolation & security boundary

Assumption: repos are operator-owned, so the threat model is "limit blast radius
and prevent secret/credential leakage," not "sandbox hostile code."

- **Process/file isolation:** one container per task; no host bind mounts beyond
  a per-task scratch volume that is destroyed on teardown.
- **Network:** restrict egress to what's needed (Anthropic API, the git host,
  package registries). Default-deny is the target; phase 1 may start permissive
  and tighten.
- **Secrets:** injected at runtime, never in the image or in logs. The captured
  transcript is scrubbed of obvious token patterns before posting to Slack.
- **Resource caps:** CPU/memory/pids limits on the container; the hard timeout
  guarantees the environment cannot run forever.
- **Teardown guarantee:** enforced by the `Runner` contract (§4.1) so a crash in
  the orchestrator still can't strand a container — `--rm` plus an unconditional
  cleanup `defer`.

## 8. Open questions

1. **Claude Code headless invocation & output format** — pinned-version flags
   for non-interactive runs and structured transcript capture. Settled in #16.
2. **Streaming vs. batch output to Slack** — live log tail vs. a single summary
   post. Start with periodic flushes; revisit.
3. **Result handoff** — does the agent push a branch + open a PR itself, or hand
   a diff back to Cortex to apply? Leaning "agent opens the PR" (it has the repo
   and toolchain), with the PR URL as the primary artifact.
4. **Concurrency limits** — a simple max-concurrent-tasks semaphore in the
   orchestrator; value TBD by host/quota.

## 9. Follow-up implementation issues

- **#16 — Build the agent runtime image.** `build/agent-runtime/Dockerfile`:
  pinned base, Claude Code, toolchain, `agent-entrypoint` that clones + runs
  Claude Code headless; published to Artifact Registry. (§4.3)
- **#17 — Lifecycle orchestration.** `internal/runtime` with the `Runner`
  interface and `DockerRunner`; guaranteed teardown; wired into
  `cmd/cortex/main.go` and the trigger path. (§4.1, §4.2, §5)
- **New — Cloud Run Jobs backend.** `CloudRunJobsRunner` implementing `Runner`;
  Secret Manager mounts; Terraform for the Job + IAM. (§3, phase 2)
- **New — Trigger & Slack surface.** Slash command / signal handling to start a
  task, stream transcript to the thread, and post the final summary. (§6)
