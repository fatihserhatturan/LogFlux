.PHONY: build test test-coverage bench clean run lint fmt deps docker-build docker-run help

BINARY_NAME=logflux
BUILD_DIR=bin
IMAGE_NAME=logflux

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

build: ## Build the application
	@echo "Building..."
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/collector

run: ## Run the application
	@echo "Running..."
	go run ./cmd/collector $(ARGS)

test: ## Run tests
	@echo "Testing..."
	go test -v -race -coverprofile=coverage.out ./...

test-coverage: test ## Run tests with coverage
	go tool cover -html=coverage.out

bench: ## Run benchmarks
	go test -bench=. -benchmem ./...

lint: ## Run linter
	@echo "Linting..."
	golangci-lint run

fmt: ## Format code
	gofmt -w .

clean: ## Clean build files
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out

deps: ## Download dependencies
	go mod download
	go mod tidy

docker-build: ## Build Docker image
	docker build -t $(IMAGE_NAME) .

docker-run: ## Run Docker image (HTTP ingest on :8080, UI on :9090)
	docker run --rm -p 8080:8080 -p 9090:9090 -v logflux-data:/data $(IMAGE_NAME)

.DEFAULT_GOAL := help