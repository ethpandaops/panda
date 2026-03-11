.PHONY: build build-server build-panda build-proxy install install-server install-panda install-proxy test lint clean docker docker-push docker-sandbox test-sandbox run help download-models clean-models setup-hooks

# Embedding model configuration
MODELS_DIR := ./models
MODEL_DIR := $(MODELS_DIR)/all-MiniLM-L6-v2
HF_BASE := https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -s -w \
	-X github.com/ethpandaops/panda/internal/version.Version=$(VERSION) \
	-X github.com/ethpandaops/panda/internal/version.GitCommit=$(GIT_COMMIT) \
	-X github.com/ethpandaops/panda/internal/version.BuildTime=$(BUILD_TIME)

# Docker variables
DOCKER_IMAGE ?= ethpandaops/panda
DOCKER_TAG ?= $(VERSION)

# Go variables
GOBIN ?= $(shell go env GOPATH)/bin

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: build-server build-panda ## Build primary binaries (panda-server + panda)

build-server: ## Build the server binary
	go build -ldflags "$(LDFLAGS)" -o panda-server ./cmd/server

build-panda: ## Build the CLI binary
	go build -ldflags "$(LDFLAGS)" -o panda ./cmd/panda

build-proxy: ## Build the standalone proxy binary
	go build -ldflags "$(LDFLAGS)" -o panda-proxy ./cmd/proxy

build-linux: ## Build for Linux (amd64)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o panda-server-linux-amd64 ./cmd/server

test: ## Run tests
	go test -race -v ./...

test-coverage: ## Run tests with coverage
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run linters
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint v2..." && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)
	golangci-lint run ./...

lint-fix: ## Run linters and fix issues
	golangci-lint run --fix ./...

fmt: ## Format code
	go fmt ./...
	gofmt -s -w .

vet: ## Run go vet
	go vet ./...

tidy: ## Run go mod tidy
	go mod tidy

clean: ## Clean build artifacts
	rm -f panda-server .panda-server-bin panda panda-proxy panda-server-linux-amd64
	rm -f coverage.out coverage.html

docker: ## Build Docker image
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) \
		-t $(DOCKER_IMAGE):latest \
		.

docker-push: docker ## Push Docker image
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)
	docker push $(DOCKER_IMAGE):latest

docker-sandbox: ## Build sandbox Docker image
	docker build -t ethpandaops-panda-sandbox:latest -f sandbox/Dockerfile .

test-sandbox: ## Run sandbox package tests
	go test -race -v ./pkg/sandbox/...

run: build-server download-models ## Run the server with stdio transport
	./panda-server serve

run-sse: build-server ## Run the server with SSE transport
	./panda-server serve --transport sse --port 2480

run-docker: docker ## Run with docker compose
	docker compose up -d

stop-docker: ## Stop docker compose services
	docker compose down

logs: ## View docker compose logs
	docker compose logs -f panda-server

install: install-server install-panda ## Install primary binaries to GOBIN

install-server: ## Install the server binary to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/panda-server ./cmd/server

install-panda: ## Install the CLI binary to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/panda ./cmd/panda

install-proxy: ## Install the standalone proxy binary to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/panda-proxy ./cmd/proxy

setup-hooks: ## Install git pre-commit hooks
	git config core.hooksPath .githooks
	@echo "Git hooks configured to use .githooks/"

version: ## Show version info
	@echo "Version:    $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

download-models: $(MODEL_DIR)/model.onnx ## Download embedding model from HuggingFace
	@echo "Model downloaded to $(MODEL_DIR)"

$(MODEL_DIR)/model.onnx:
	@mkdir -p $(MODEL_DIR)
	@echo "Downloading all-MiniLM-L6-v2 ONNX model from HuggingFace..."
	@curl -sL -o $(MODEL_DIR)/model.onnx $(HF_BASE)/onnx/model.onnx
	@curl -sL -o $(MODEL_DIR)/tokenizer.json $(HF_BASE)/tokenizer.json
	@curl -sL -o $(MODEL_DIR)/config.json $(HF_BASE)/config.json
	@curl -sL -o $(MODEL_DIR)/special_tokens_map.json $(HF_BASE)/special_tokens_map.json
	@curl -sL -o $(MODEL_DIR)/tokenizer_config.json $(HF_BASE)/tokenizer_config.json
	@echo "Model files downloaded to $(MODEL_DIR)"

clean-models: ## Clean downloaded models
	rm -rf $(MODELS_DIR)
