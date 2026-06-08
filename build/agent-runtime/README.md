# cortex-agent-runtime image

The container image a disposable agent task runs inside: **Claude Code + a
build/debug toolchain**, so the container can clone a repo and run `make`/tests
against it. Design: [`docs/agent-runtime.md`](../../docs/agent-runtime.md) (§4.3).
Tracks issue #16; consumed by the orchestrator (issue #17).

## Contents

- `Dockerfile` — pinned Node base + Go, `git`, `make`, `protoc` (+ Go plugins),
  and Claude Code. Versions are build `ARG`s; the Go tarball is checksum-verified.
- `agent-entrypoint.sh` — clones the repo at a ref, runs Claude Code headless,
  and writes a transcript + `result.json`. Driven entirely by environment
  variables (see the header comment in the script for the full contract).

## Build

```bash
# from the repo root
make agent-image                       # -> cortex-agent-runtime:dev
# or directly
docker build -t cortex-agent-runtime:dev build/agent-runtime
```

Multi-arch (amd64/arm64) is supported via buildx, which sets `TARGETARCH`:

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
    -t <registry>/cortex-agent-runtime:<tag> build/agent-runtime
```

## Run a one-off task (manual)

```bash
docker run --rm \
    -e ANTHROPIC_API_KEY \
    -e AGENT_REPO=mjcramer/cortex \
    -e AGENT_PROMPT="Run the test suite and summarize any failures." \
    -e CORTEX_CLAUDE_MODEL=claude-sonnet-4-6 \
    -v "$PWD/out:/workspace/out" \
    cortex-agent-runtime:dev
```

Results land in `./out/`: `transcript.jsonl`, `result.json`, and `*.diff`.

## Pinning

`Dockerfile` ARGs pin Go (`1.26.3`, checksum-verified), Claude Code
(`2.1.168`), and protoc (`35.0`). The base image is tag-pinned; CI should
additionally digest-pin it (`FROM node@sha256:...`) and publish to Artifact
Registry, per the design doc.
