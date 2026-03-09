.PHONY: build build-mcp build-cli build-search build-proxy install install-mcp install-cli install-search install-proxy install-search-assets test lint clean docker docker-push docker-sandbox test-sandbox run help download-models clean-models setup-hooks

# Embedding model and shared library configuration
# Downloaded from HuggingFace and kelindar/search GitHub repo
MODELS_DIR := ./models
EMBEDDING_MODEL_PATH := $(MODELS_DIR)/MiniLM-L6-v2.Q8_0.gguf
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
LLAMA_LIB_FILENAME := libllama_go.dylib
LLAMA_LIB_GLOB := libllama_go*.dylib
else
LLAMA_LIB_FILENAME := libllama_go.so
LLAMA_LIB_GLOB := libllama_go.so*
endif
LLAMA_LIB_PATH := $(MODELS_DIR)/$(LLAMA_LIB_FILENAME)

# Download URLs
EMBEDDING_MODEL_URL := https://huggingface.co/second-state/All-MiniLM-L6-v2-Embedding-GGUF/resolve/main/all-MiniLM-L6-v2-Q8_0.gguf

# Source build directory for libllama_go.so
LLAMA_BUILD_DIR := $(MODELS_DIR)/llama-build

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -s -w \
	-X github.com/ethpandaops/mcp/internal/version.Version=$(VERSION) \
	-X github.com/ethpandaops/mcp/internal/version.GitCommit=$(GIT_COMMIT) \
	-X github.com/ethpandaops/mcp/internal/version.BuildTime=$(BUILD_TIME)

# Docker variables
DOCKER_IMAGE ?= ethpandaops/mcp
DOCKER_TAG ?= $(VERSION)

# Go variables
GOBIN ?= $(shell go env GOPATH)/bin

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: build-mcp build-cli build-search ## Build primary binaries (mcp + ep + ep-search)

build-mcp: ## Build the MCP server binary
	go build -ldflags "$(LDFLAGS)" -o mcp ./cmd/mcp

build-cli: ## Build the CLI binary
	go build -ldflags "$(LDFLAGS)" -o ep ./cmd/cli

build-search: ## Build the CLI search helper binary
	go build -ldflags "$(LDFLAGS)" -o ep-search ./cmd/ep-search

build-proxy: ## Build the standalone proxy binary
	go build -ldflags "$(LDFLAGS)" -o proxy ./cmd/proxy

build-linux: ## Build for Linux (amd64)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o mcp-linux-amd64 ./cmd/mcp

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
	rm -f mcp ep ep-search proxy mcp-linux-amd64
	rm -f libllama_go.so libllama_go.dylib
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
	docker build -t ethpandaops-mcp-sandbox:latest -f sandbox/Dockerfile .

test-sandbox: build-mcp docker-sandbox ## Test sandbox execution (requires .env)
	@if [ -f .env ]; then \
		set -a && . .env && set +a && ./mcp test; \
	else \
		echo "Error: .env file not found. Copy .env.example and configure it."; \
		exit 1; \
	fi

run: build-mcp download-models ## Run the server with stdio transport
	./mcp serve

run-sse: build-mcp ## Run the server with SSE transport
	./mcp serve --transport sse --port 2480

run-docker: docker ## Run with docker-compose
	docker-compose up -d

stop-docker: ## Stop docker-compose services
	docker-compose down

logs: ## View docker-compose logs
	docker-compose logs -f mcp-server

install: install-mcp install-cli install-search install-search-assets ## Install primary binaries to GOBIN

install-mcp: ## Install the MCP server binary to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/mcp ./cmd/mcp

install-cli: ## Install the CLI binary to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/ep ./cmd/cli

install-search: ## Install the CLI search helper to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/ep-search ./cmd/ep-search

install-proxy: ## Install the standalone proxy binary to GOBIN
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/proxy ./cmd/proxy

install-search-assets: download-models ## Install search runtime assets next to installed binaries
	@mkdir -p $(GOBIN)/models
	cp $(EMBEDDING_MODEL_PATH) $(GOBIN)/models/MiniLM-L6-v2.Q8_0.gguf
	cp $(LLAMA_LIB_PATH) $(GOBIN)/$(LLAMA_LIB_FILENAME)

setup-hooks: ## Install git pre-commit hooks
	git config core.hooksPath .githooks
	@echo "Git hooks configured to use .githooks/"

version: ## Show version info
	@echo "Version:    $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

download-models: $(EMBEDDING_MODEL_PATH) $(LLAMA_LIB_PATH) ## Download embedding model and shared library
	@cp $(LLAMA_LIB_PATH) $(LLAMA_LIB_FILENAME)
	@echo "All models downloaded to $(MODELS_DIR)"

$(EMBEDDING_MODEL_PATH):
	@mkdir -p $(MODELS_DIR)
	@echo "Downloading embedding model from HuggingFace..."
	@curl -L -o $(EMBEDDING_MODEL_PATH) $(EMBEDDING_MODEL_URL)
	@echo "Model downloaded to $(EMBEDDING_MODEL_PATH)"

$(LLAMA_LIB_PATH):
	@mkdir -p $(LLAMA_BUILD_DIR)
	@echo "Building libllama_go.so from source (requires cmake, g++)..."
	@cd $(LLAMA_BUILD_DIR) && \
		if [ ! -d search ]; then \
			git clone --depth 1 --recurse-submodules https://github.com/kelindar/search.git; \
		fi && \
		cd search && mkdir -p build && cd build && \
			cmake -DBUILD_SHARED_LIBS=ON -DCMAKE_BUILD_TYPE=Release \
			-DGGML_NATIVE=OFF \
			-DCMAKE_CXX_COMPILER=g++ -DCMAKE_C_COMPILER=gcc .. && \
		cmake --build . --config Release
	@lib=$$(find $(LLAMA_BUILD_DIR)/search/build -type f -name '$(LLAMA_LIB_GLOB)' | head -n 1); \
		if [ -z "$$lib" ]; then \
			echo "Failed to locate built shared library matching $(LLAMA_LIB_GLOB)"; \
			exit 1; \
		fi; \
		cp "$$lib" $(LLAMA_LIB_PATH)
	@echo "Shared library built at $(LLAMA_LIB_PATH)"

clean-models: ## Clean downloaded models and build artifacts
	rm -rf $(MODELS_DIR) libllama_go.so libllama_go.dylib
