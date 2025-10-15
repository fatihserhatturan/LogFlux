.PHONY: build test clean run lint help

BINARY_NAME=logflux
BUILD_DIR=bin

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

build: ## Build the application
	@echo "Building..."
	go build -o $(BUILD_DIR)/$(BINARY_NAME) cmd/collector/main.go

run: ## Run the application
	@echo "Running..."
	go run cmd/collector/main.go

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

clean: ## Clean build files
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out

deps: ## Download dependencies
	go mod download
	go mod tidy

.DEFAULT_GOAL := help