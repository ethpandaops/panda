.PHONY: build test lint clean docker docker-push docker-sandbox test-sandbox run help download-models clean-models

# Embedding model and shared library configuration
# Downloaded from HuggingFace and kelindar/search GitHub repo
MODELS_DIR := ./models
EMBEDDING_MODEL_PATH := $(MODELS_DIR)/MiniLM-L6-v2.Q8_0.gguf
LLAMA_SO_PATH := $(MODELS_DIR)/libllama_go.so

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

build: ## Build the MCP server binary
	go build -ldflags "$(LDFLAGS)" -o mcp ./cmd/mcp

build-cli: ## Build the CLI binary
	go build -ldflags "$(LDFLAGS)" -o ep ./cmd/cli

build-linux: ## Build for Linux (amd64)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o mcp-linux-amd64 ./cmd/mcp

test: ## Run tests
	go test -race -v ./...

test-coverage: ## Run tests with coverage
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run linters
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
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
	rm -f mcp ep mcp-linux-amd64
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

test-sandbox: build docker-sandbox ## Test sandbox execution (requires .env)
	@if [ -f .env ]; then \
		set -a && . .env && set +a && ./mcp test; \
	else \
		echo "Error: .env file not found. Copy .env.example and configure it."; \
		exit 1; \
	fi

run: build download-models ## Run the server with stdio transport
	./mcp serve

run-sse: build ## Run the server with SSE transport
	./mcp serve --transport sse --port 2480

run-docker: docker ## Run with docker-compose
	docker-compose up -d

stop-docker: ## Stop docker-compose services
	docker-compose down

logs: ## View docker-compose logs
	docker-compose logs -f mcp-server

install: build ## Install binary to GOBIN
	cp mcp $(GOBIN)/mcp

version: ## Show version info
	@echo "Version:    $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

download-models: $(EMBEDDING_MODEL_PATH) $(LLAMA_SO_PATH) ## Download embedding model and shared library
	@ln -sf $(LLAMA_SO_PATH) libllama_go.so
	@echo "All models downloaded to $(MODELS_DIR)"

$(EMBEDDING_MODEL_PATH):
	@mkdir -p $(MODELS_DIR)
	@echo "Downloading embedding model from HuggingFace..."
	@curl -L -o $(EMBEDDING_MODEL_PATH) $(EMBEDDING_MODEL_URL)
	@echo "Model downloaded to $(EMBEDDING_MODEL_PATH)"

$(LLAMA_SO_PATH):
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
	@cp $(LLAMA_BUILD_DIR)/search/build/lib/libllama_go.so.1.0 $(LLAMA_SO_PATH)
	@echo "Shared library built at $(LLAMA_SO_PATH)"

clean-models: ## Clean downloaded models and build artifacts
	rm -rf $(MODELS_DIR) libllama_go.so
