# syntax=docker/dockerfile:1
#
# cortex-agent-runtime: the disposable environment an agent task runs inside.
#
# Bundles Claude Code plus the build/compile/debug toolchain the target repos
# need, so the container can clone a repo and run `make`/tests against it. See
# docs/agent-runtime.md (§4.3) for the design. Used by the orchestrator in
# internal/runtime (issue #17); each task gets one container that is torn down
# on completion.
#
# Build (multi-arch via buildx sets TARGETARCH automatically):
#   docker build -t cortex-agent-runtime:dev build/agent-runtime
#
# Pinning: versions are ARGs so they are visible and overridable, and the Go
# tarball is checksum-verified. The base image is pinned by tag here; CI should
# additionally digest-pin it (FROM node@sha256:...) for full reproducibility.

ARG NODE_VERSION=22-bookworm-slim
FROM node:${NODE_VERSION}

# --- Versions (pinned) -------------------------------------------------------
ARG GO_VERSION=1.26.3
ARG GO_SHA256_AMD64=2b2cfc7148493da5e73981bffbf3353af381d5f93e789c82c79aff64962eb556
ARG GO_SHA256_ARM64=9d89a3ea57d141c2b22d70083f2c8459ba3890f2d9e818e7e933b75614936565
ARG CLAUDE_CODE_VERSION=2.1.168
ARG PROTOC_VERSION=35.0
ARG PROTOC_GEN_GO_VERSION=v1.36.11
ARG PROTOC_GEN_GO_GRPC_VERSION=v1.5.1

# TARGETARCH is provided by the builder (amd64 | arm64).
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive

# --- Base OS toolchain -------------------------------------------------------
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        make \
        unzip \
        build-essential \
        jq \
        ripgrep \
        less \
        openssh-client; \
    rm -rf /var/lib/apt/lists/*

# --- Go (pinned + checksum-verified) ----------------------------------------
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) goarch=amd64; gosha="${GO_SHA256_AMD64}" ;; \
        arm64) goarch=arm64; gosha="${GO_SHA256_ARM64}" ;; \
        *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    url="https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz"; \
    curl -fsSL -o /tmp/go.tar.gz "${url}"; \
    echo "${gosha}  /tmp/go.tar.gz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    rm /tmp/go.tar.gz; \
    /usr/local/go/bin/go version

ENV PATH=/usr/local/go/bin:/root/go/bin:${PATH}
ENV GOPATH=/root/go
ENV GOTOOLCHAIN=local

# --- protoc + Go plugins (only needed for `make proto`) ----------------------
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) parch=x86_64 ;; \
        arm64) parch=aarch_64 ;; \
        *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    url="https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${parch}.zip"; \
    curl -fsSL -o /tmp/protoc.zip "${url}"; \
    unzip -q /tmp/protoc.zip -d /usr/local/protoc; \
    ln -s /usr/local/protoc/bin/protoc /usr/local/bin/protoc; \
    rm /tmp/protoc.zip; \
    protoc --version; \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}; \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}; \
    protoc-gen-go --version; \
    protoc-gen-go-grpc --version || true

# --- Claude Code (pinned) ----------------------------------------------------
RUN set -eux; \
    npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}; \
    claude --version

# --- Non-root user the task runs as -----------------------------------------
# A disposable, operator-owned sandbox; running as non-root limits blast radius.
RUN set -eux; \
    useradd --create-home --shell /bin/bash agent; \
    mkdir -p /workspace; \
    chown agent:agent /workspace

COPY agent-entrypoint.sh /usr/local/bin/agent-entrypoint
RUN chmod +x /usr/local/bin/agent-entrypoint

USER agent
# Give the agent user its own Go tool cache/bin and ensure global npm bin + Go
# are on PATH for shells the agent spawns.
ENV HOME=/home/agent
ENV GOPATH=/home/agent/go
ENV PATH=/usr/local/go/bin:/home/agent/go/bin:${PATH}
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/agent-entrypoint"]
