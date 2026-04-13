.PHONY: help build run cli test test-e2e test-all vet lint fmt check clean create-wallets

.DEFAULT_GOAL := help

## help: Show this help message
help:
	@echo "Aptos Contract API (Rewrite)"
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
	@echo ""
	@echo "Getting started:"
	@echo "  1. cp .env.example .env      # configure MYSQL_DSN, API_KEY, Circle, Aptos"
	@echo "  2. make create-wallets        # create Circle wallets"
	@echo "  3. Fund wallet on testnet     # https://aptos.dev/en/network/faucet"
	@echo "  4. make run                   # start the server (runs DB migrations)"
	@echo "  5. make test-e2e              # run e2e tests (in another terminal)"

## build: Build server and CLI binaries to bin/
build:
	go build -o bin/server ./cmd/server
	go build -o bin/cli ./cmd/cli

## run: Run the server (loads .env automatically)
run:
	go run ./cmd/server

## cli: Run the CLI (pass ARGS, e.g. make cli ARGS="health")
cli:
	go run ./cmd/cli $(ARGS)

## test: Run unit tests
test:
	go test ./internal/... -v

## test-e2e: Run e2e tests (requires server running + MySQL)
test-e2e:
	go test -tags=e2e ./examples/ -v -count=1

## test-all: Run unit + e2e tests
test-all: test test-e2e

## fmt: Format code with gofumpt
fmt:
	gofumpt -w .

## vet: Run go vet
vet:
	go vet ./...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## check: Format + vet + lint + unit tests
check: fmt vet lint test

## create-wallets: Create Circle wallets on Aptos testnet
create-wallets:
	go run ./examples/create_wallets -count 1

## clean: Remove build artifacts
clean:
	rm -rf bin/
