.DEFAULT_GOAL := help

SHELL := /bin/sh

GO ?= go
PKG ?= ./...
BIN ?= cortex
CMD ?= ./cmd/cortex

BUILD_FLAGS ?=
TEST_FLAGS ?=
RUN_ARGS ?=

TUNNEL_PORT ?= 23001

PROTOC ?= protoc
PROTO_DIR ?= proto
PROTO_FILES := $(wildcard $(PROTO_DIR)/*.proto)
PROTO_OUT ?= .

.PHONY: help build release run check test vet fmt fmt-check lint clean tidy proto tunnel ci toolchain

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_.-]+:.*## / {printf "%-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the cortex binary into ./bin/$(BIN)
	mkdir -p bin
	$(GO) build $(BUILD_FLAGS) -o bin/$(BIN) $(CMD)

release: ## Build a stripped, statically linked release binary
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -trimpath -ldflags="-s -w" -o bin/$(BIN) $(CMD)

run: ## Run the cortex server
	$(GO) run $(CMD) $(RUN_ARGS)

check: ## Vet and build to catch obvious mistakes
	$(GO) vet $(PKG)
	$(GO) build $(BUILD_FLAGS) $(PKG)

test: ## Run unit and integration tests
	$(GO) test $(TEST_FLAGS) $(PKG)

vet: ## Run go vet
	$(GO) vet $(PKG)

fmt: ## Format Go sources in place
	$(GO) fmt $(PKG)

fmt-check: ## Verify Go formatting
	@out=`gofmt -l .`; if [ -n "$$out" ]; then echo "needs gofmt:" && echo "$$out" && exit 1; fi

lint: ## Run golangci-lint if available
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

proto: ## Regenerate protobuf bindings from $(PROTO_DIR)
	$(PROTOC) \
		--go_out=$(PROTO_OUT) --go_opt=module=github.com/mjcramer/cortex \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=module=github.com/mjcramer/cortex \
		--proto_path=$(PROTO_DIR) \
		$(PROTO_FILES)

clean: ## Remove build artifacts
	rm -rf bin

tunnel: ## Open a public ngrok tunnel to the local server on TUNNEL_PORT
	@command -v ngrok >/dev/null 2>&1 || { echo "ngrok not found; install via 'brew install ngrok'"; exit 1; }
	ngrok http --url=scarf-flavorful-contented.ngrok-free.dev $(TUNNEL_PORT)

SLACK_MANIFEST ?= integrations/slack/manifest.yaml

slack-sync: ## Push $(SLACK_MANIFEST) to the Slack app via apps.manifest.update
	@for tool in yq jq curl sed; do \
		command -v $$tool >/dev/null 2>&1 || { echo "missing tool: $$tool (install with: brew install yq jq)"; exit 1; }; \
	done
	@: $${SLACK_CONFIG_ACCESS_TOKEN:?required — 12h tooling token from api.slack.com → Your Apps → Manage app config tokens}
	@: $${SLACK_APP_ID:?required — e.g. A0123456789}
	@: $${SLACK_CALLBACK_URL:?required — e.g. https://your.ngrok-free.dev (no trailing slash)}
	@sed "s|\$${SLACK_CALLBACK_URL}|$$SLACK_CALLBACK_URL|g" $(SLACK_MANIFEST) \
	  | yq -o json '.' \
	  | jq --arg id "$$SLACK_APP_ID" '{app_id: $$id, manifest: .}' \
	  | curl -sS -X POST https://slack.com/api/apps.manifest.update \
	      -H "Authorization: Bearer $$SLACK_CONFIG_ACCESS_TOKEN" \
	      -H "Content-Type: application/json; charset=utf-8" \
	      -d @- \
	  | jq .

ci: fmt-check vet test ## Run the standard local CI checks

toolchain: ## Print tool versions used by the project
	$(GO) version
	$(PROTOC) --version
	@command -v protoc-gen-go >/dev/null 2>&1 && protoc-gen-go --version || true
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 && protoc-gen-go-grpc --version || true
