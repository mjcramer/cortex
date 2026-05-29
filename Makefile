.DEFAULT_GOAL := help

SHELL := /bin/sh

GO ?= go
PKG ?= ./...
BIN ?= cortex
CMD ?= ./cmd/cortex

BUILD_FLAGS ?=
TEST_FLAGS ?=
RUN_ARGS ?=

TUNNEL_PORT ?= 50051

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

tunnel: ## Open a public cloudflared tunnel to the local server on TUNNEL_PORT
	@command -v cloudflared >/dev/null 2>&1 || { echo "cloudflared not found; install via 'brew install cloudflared'"; exit 1; }
	cloudflared tunnel --url http://localhost:$(TUNNEL_PORT)

ci: fmt-check vet test ## Run the standard local CI checks

toolchain: ## Print tool versions used by the project
	$(GO) version
	$(PROTOC) --version
	@command -v protoc-gen-go >/dev/null 2>&1 && protoc-gen-go --version || true
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 && protoc-gen-go-grpc --version || true
