.PHONY: help build run cli migrate test test-e2e test-all vet lint fmt check clean create-wallets \
	db-up db-down db-reset db-logs db-shell

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
	@echo "  2. make db-up                 # start MySQL via docker-compose"
	@echo "  3. make migrate               # (optional) apply schema without starting server"
	@echo "  4. make create-wallets        # create Circle wallets"
	@echo "  5. Fund wallet on testnet     # https://aptos.dev/en/network/faucet"
	@echo "  6. make run                   # start the server (also runs migrations)"
	@echo "  7. make test-e2e              # run e2e tests (in another terminal)"

## build: Build server and CLI binaries to bin/
build:
	go build -o bin/server ./cmd/server
	go build -o bin/cli ./cmd/cli

## run: Run the server (loads .env automatically)
run:
	go run ./cmd/server

## migrate: Apply DB migrations without starting the server (uses MYSQL_DSN)
migrate:
	go run ./cmd/migrate

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

## clean: Remove build artifacts
clean:
	rm -rf bin/

## db-up: Start MySQL in the background via docker-compose
db-up:
	docker compose up -d mysql
	@echo "Waiting for MySQL to be healthy..."
	@for i in $$(seq 1 30); do \
		status=$$(docker inspect --format='{{.State.Health.Status}}' $$(docker compose ps -q mysql) 2>/dev/null); \
		if [ "$$status" = "healthy" ]; then echo "MySQL is healthy."; exit 0; fi; \
		sleep 1; \
	done; \
	echo "MySQL did not become healthy within 30s"; exit 1

## db-down: Stop MySQL (keeps data volume)
db-down:
	docker compose down

## db-reset: Wipe MySQL data volume and recreate a fresh database
db-reset:
	docker compose down -v
	$(MAKE) db-up
	$(MAKE) migrate

## db-logs: Tail MySQL container logs
db-logs:
	docker compose logs -f mysql

## db-shell: Open a MySQL shell against the local container
db-shell:
	docker compose exec mysql mysql -uroot -proot contract_api
