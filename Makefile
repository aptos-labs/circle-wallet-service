# Contract API — build, test, lint, format, run

# Default binary output directory
BIN_DIR := bin

# Go module
MODULE := github.com/aptos-labs/jc-contract-integration

.PHONY: help build test test-race test-verbose vet lint fmt fmt-check run check e2e smoke-test localnet-test example clean

## help: Show this help message (default target)
help:
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'

## build: Compile server and openapi binaries into bin/
build:
	go build -o $(BIN_DIR)/server ./cmd/server
	go build -o $(BIN_DIR)/openapi ./cmd/openapi

## test: Run all tests
test:
	go test ./...

## test-race: Run all tests with race detector
test-race:
	go test -race -count=1 ./...

## test-verbose: Run all tests with verbose output
test-verbose:
	go test -race -count=1 -v ./...

## vet: Run go vet
vet:
	go vet ./...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## fmt: Format code with gofumpt
fmt:
	gofumpt -w .

## fmt-check: Check formatting without modifying files (useful in CI)
fmt-check:
	@test -z "$$(gofumpt -l .)" || { echo "Files need formatting:"; gofumpt -l .; exit 1; }

## run: Start the API server
run: build
	./$(BIN_DIR)/server

## check: Run all validations (fmt-check + vet + lint + test-race) — use before committing
check: fmt-check vet lint test-race

## e2e: Deploy contract to devnet and run full end-to-end tests (requires aptos CLI)
e2e:
	@command -v aptos >/dev/null 2>&1 || { echo "Error: aptos CLI not found. Install from https://aptos.dev/tools/aptos-cli"; exit 1; }
	go test -tags=e2e -v -count=1 -timeout 10m ./e2e

## smoke-test: Run curl-based smoke tests against a running server (set BASE_URL and API_KEY)
smoke-test:
	@./scripts/smoke-test.sh

## localnet-test: Start localnet, deploy contract, run full integration tests (requires aptos CLI)
localnet-test:
	@./scripts/localnet-test.sh

## example: Run the wrap-existing-contract example against a running server (set API_URL and API_KEY)
example:
	@./examples/wrap-existing-contract/demo.sh

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR)
