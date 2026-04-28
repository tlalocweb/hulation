# Hulation Makefile
#
# Usage:
#   make              - build the hula server
#   make all          - build server + all CLI tools
#   make hulactl      - build the hulactl CLI
#   make setupdb      - build the setupdb tool
#   make docker       - build multi-platform Docker image (no push)
#   make docker-push  - build and push multi-platform Docker image
#   make test         - run tests
#   make clean        - remove build artifacts

SHELL := /bin/bash

# Project
MODULE      := github.com/tlalocweb/hulation
BIN_DIR     := $(CURDIR)/.bin
EXTERNAL_DIR := $(CURDIR)/.external

# Versioning
VERSION     ?= $(shell git describe --tags 2>/dev/null || echo "dev")
BUILD_DATE  ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
LDFLAGS     := -X $(MODULE)/config.Version=$(VERSION) -X $(MODULE)/config.BuildDate=$(BUILD_DATE)

# Go configuration
# If setup-dev.sh has installed Go locally, use that; otherwise use system Go
ifneq ($(wildcard $(BIN_DIR)/go/bin/go),)
    GO := $(BIN_DIR)/go/bin/go
else
    GO := go
endif

export CGO_ENABLED := 1

# Docker
DOCKER_REGISTRY ?= ghcr.io
DOCKER_REPO     ?= tlalocweb/hula
DOCKER_TAG      ?= $(VERSION)
DOCKER_IMAGE    := $(DOCKER_REGISTRY)/$(DOCKER_REPO)
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64
# Docker context must be parent dir because of replace directive: ../clickhouse
DOCKER_CONTEXT  := $(CURDIR)/..
DOCKERFILE      := $(CURDIR)/Dockerfile
BUILDX_BUILDER  := hula-builder

# Build targets
HULA_BIN      := $(BIN_DIR)/hula
HULACTL_BIN   := $(BIN_DIR)/hulactl
SETUPDB_BIN   := $(BIN_DIR)/setupdb
HULABUILD_BIN := $(BIN_DIR)/hulabuild

# ============================================================================
# Primary targets
# ============================================================================

.PHONY: all build hula hulactl setupdb hulabuild tools
.PHONY: docker docker-push docker-local
.PHONY: test test-unit test-verbose vet lint
.PHONY: clean clean-docker
.PHONY: deps version help
.PHONY: protobuf protobuf-clean protoc-install

## build: Build the hula server binary (default)
build: hula

## all: Build server and all CLI tools
all: hula tools

## tools: Build all CLI tools (hulactl, setupdb, hulabuild)
tools: hulactl setupdb hulabuild

# ============================================================================
# Go binaries
# ============================================================================

## hula: Build the hula server
hula: $(HULA_BIN)

$(HULA_BIN): $(shell find . -name '*.go' -not -path './.external/*' -not -path './.bin/*' -not -path './.gopath/*') go.mod go.sum | $(BIN_DIR)
	@echo "Building hula server $(VERSION)..."
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ .

## hulactl: Build the hulactl CLI tool
hulactl: $(HULACTL_BIN)

$(HULACTL_BIN): $(shell find . -name '*.go' -not -path './.external/*' -not -path './.bin/*' -not -path './.gopath/*') go.mod go.sum | $(BIN_DIR)
	@echo "Building hulactl..."
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./model/tools/hulactl

## setupdb: Build the setupdb tool
setupdb: $(SETUPDB_BIN)

$(SETUPDB_BIN): $(shell find . -name '*.go' -not -path './.external/*' -not -path './.bin/*' -not -path './.gopath/*') go.mod go.sum | $(BIN_DIR)
	@echo "Building setupdb..."
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./model/tools/setupdb

## hulabuild: Build the hulabuild binary (runs inside builder containers)
hulabuild: $(HULABUILD_BIN)

$(HULABUILD_BIN): $(shell find . -name '*.go' -not -path './.external/*' -not -path './.bin/*' -not -path './.gopath/*') go.mod go.sum | $(BIN_DIR)
	@echo "Building hulabuild..."
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./model/tools/hulabuild

$(BIN_DIR):
	@mkdir -p $(BIN_DIR)

# ============================================================================
# Docker
# ============================================================================

## docker: Build multi-platform Docker image (local only, no push)
docker:
	@echo "Building Docker image $(DOCKER_IMAGE):$(DOCKER_TAG) for $(DOCKER_PLATFORMS)..."
	docker buildx create --use --name $(BUILDX_BUILDER) --platform $(DOCKER_PLATFORMS) 2>/dev/null || true
	docker buildx inspect --bootstrap
	docker buildx build \
		-f $(DOCKERFILE) \
		--build-arg hulaversion=$(VERSION) \
		--build-arg hulabuilddate=$(BUILD_DATE) \
		--platform $(DOCKER_PLATFORMS) \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):latest \
		$(DOCKER_CONTEXT)

## docker-push: Build and push multi-platform Docker image to registry
docker-push:
	@echo "Building and pushing Docker image $(DOCKER_IMAGE):$(DOCKER_TAG)..."
	docker buildx create --use --name $(BUILDX_BUILDER) --platform $(DOCKER_PLATFORMS) 2>/dev/null || true
	docker buildx inspect --bootstrap
	docker buildx build \
		-f $(DOCKERFILE) \
		--build-arg hulaversion=$(VERSION) \
		--build-arg hulabuilddate=$(BUILD_DATE) \
		--platform $(DOCKER_PLATFORMS) \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):latest \
		--push \
		$(DOCKER_CONTEXT)

## docker-local: Build Docker image for local platform only (faster, loads into docker)
docker-local:
	@echo "Building Docker image $(DOCKER_IMAGE):$(DOCKER_TAG) for local platform..."
	docker build \
		-f $(CURDIR)/Dockerfile.local \
		--build-arg hulaversion=$(VERSION) \
		--build-arg hulabuilddate=$(BUILD_DATE) \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):latest \
		$(DOCKER_CONTEXT)

# ============================================================================
# Protobuf / gRPC
# ============================================================================

# Folders containing .proto files whose generated code is checked into the repo.
# Note: $(PROTOBUF_SRCS) is computed at Make time using shell find. New .proto
# files are picked up automatically on the next invocation.
PROTOBUF_FOLDERS = protoext/hula/auth \
                   pkg/server/authware/proto \
                   pkg/apiobjects/v1 \
                   pkg/apispec/v1 \
                   pkg/store/storage/raft
PROTOBUF_SRCS = $(shell find $(PROTOBUF_FOLDERS) -name '*.proto' 2>/dev/null)
PROTOBUF_OUTS = $(PROTOBUF_SRCS:.proto=.pb.go)

PROTOC       := $(BIN_DIR)/protoc
PROTOC_INC   := -I. -I./protoext -I$(BIN_DIR)/include

## protoc-install: Install pinned protoc + plugins into .bin/
protoc-install:
	@bash $(CURDIR)/hack/install-protoc.sh

## protobuf: Regenerate Go code from all .proto files
protobuf: $(PROTOBUF_OUTS)

# Per-folder rules. Each folder has slightly different generation requirements
# (gotag for tag injection, grpc for services, gateway for REST).

# protoext/hula/auth — annotation extension, Go only
protoext/hula/auth/%.pb.go: protoext/hula/auth/%.proto | $(PROTOC)
	@echo "protoc (extension): $<"
	@PATH="$(BIN_DIR):$$PATH" $(PROTOC) $(PROTOC_INC) \
		--go_out=. --go_opt=paths=source_relative \
		$<

# pkg/server/authware/proto — annotation extension, Go only
pkg/server/authware/proto/%.pb.go: pkg/server/authware/proto/%.proto | $(PROTOC)
	@echo "protoc (extension): $<"
	@PATH="$(BIN_DIR):$$PATH" $(PROTOC) $(PROTOC_INC) \
		--go_out=. --go_opt=paths=source_relative \
		$<

# pkg/apiobjects/v1 — message types with optional gotag injection
pkg/apiobjects/v1/%.pb.go: pkg/apiobjects/v1/%.proto | $(PROTOC)
	@echo "protoc (apiobjects): $<"
	@PATH="$(BIN_DIR):$$PATH" $(PROTOC) $(PROTOC_INC) \
		--go_out=. --go_opt=paths=source_relative \
		$<
	@if grep -q '@gotags' $<; then \
		PATH="$(BIN_DIR):$$PATH" $(PROTOC) $(PROTOC_INC) --gotag_out=paths=source_relative:. $<; \
	fi

# pkg/apispec/v1 — gRPC services with REST gateway
pkg/apispec/v1/%.pb.go: pkg/apispec/v1/%.proto | $(PROTOC)
	@echo "protoc (service): $<"
	@PATH="$(BIN_DIR):$$PATH" $(PROTOC) $(PROTOC_INC) \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
		$<

# pkg/store/storage/raft — internal Raft FSM command format. Messages only.
pkg/store/storage/raft/%.pb.go: pkg/store/storage/raft/%.proto | $(PROTOC)
	@echo "protoc (raftcmd): $<"
	@PATH="$(BIN_DIR):$$PATH" $(PROTOC) $(PROTOC_INC) \
		--go_out=. --go_opt=paths=source_relative \
		$<

$(PROTOC):
	@echo ""
	@echo "protoc not installed. Run: make protoc-install"
	@echo ""
	@exit 1

## protobuf-clean: Remove generated .pb.go files
protobuf-clean:
	@find $(PROTOBUF_FOLDERS) -type f \( -name '*.pb.go' -o -name '*.pb.gw.go' \) -print -delete 2>/dev/null || true

# ============================================================================
# Testing & Quality
# ============================================================================

## test: Run all tests
test:
	$(GO) test ./... -count=1

## test-unit: Run tests that don't require external services
test-unit:
	$(GO) test ./utils/... ./hooks/... -count=1

## test-verbose: Run all tests with verbose output
test-verbose:
	$(GO) test ./... -count=1 -v

## vet: Run go vet
vet:
	$(GO) vet ./...

## lint: Run golangci-lint (if installed)
lint:
	@if [ -f "$(BIN_DIR)/golangci-lint" ]; then \
		$(BIN_DIR)/golangci-lint run ./...; \
	elif command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found. Run setup-dev.sh or install it manually."; \
		exit 1; \
	fi

# ============================================================================
# Dependencies & Maintenance
# ============================================================================

## deps: Download Go module dependencies
deps:
	$(GO) mod download

## tidy: Run go mod tidy
tidy:
	$(GO) mod tidy

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR)/hula $(BIN_DIR)/hulactl $(BIN_DIR)/setupdb $(BIN_DIR)/hulabuild

## clean-all: Remove all generated files (binaries, external downloads)
clean-all:
	rm -rf $(BIN_DIR) $(EXTERNAL_DIR) $(CURDIR)/.gopath

## clean-docker: Remove the buildx builder
clean-docker:
	docker buildx rm $(BUILDX_BUILDER) 2>/dev/null || true

## version: Print version info
version:
	@echo "Version:    $(VERSION)"
	@echo "Build Date: $(BUILD_DATE)"
	@echo "Go:         $(shell $(GO) version)"
	@echo "Module:     $(MODULE)"

# ============================================================================
# ClickHouse (dev helpers)
# ============================================================================

## clickhouse-start: Start ClickHouse container for development
clickhouse-start:
	@bash $(CURDIR)/start-clickhouse.sh

## clickhouse-test: Start ClickHouse container for tests
clickhouse-test:
	@bash $(CURDIR)/start-clickhouse-for-test.sh

# ============================================================================
# Help
# ============================================================================

## help: Show this help message
help:
	@echo "Hulation Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'
	@echo ""
	@echo "Variables (override with VAR=value):"
	@echo "  VERSION            Git tag or 'dev' (current: $(VERSION))"
	@echo "  DOCKER_REGISTRY    Container registry (current: $(DOCKER_REGISTRY))"
	@echo "  DOCKER_REPO        Image repository (current: $(DOCKER_REPO))"
	@echo "  DOCKER_TAG         Image tag (current: $(DOCKER_TAG))"
	@echo "  DOCKER_PLATFORMS   Target platforms (current: $(DOCKER_PLATFORMS))"
