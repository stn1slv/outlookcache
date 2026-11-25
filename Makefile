VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CMD := outlookcache
BIN := bin/$(CMD)

.PHONY: setup test lint format build run clean upgrade-deps vulncheck

setup: ## Download dependencies and tools
	go mod download

upgrade-deps: ## Update dependencies and pinned tools
	go get -u ./... tool
	go mod tidy

test: ## Run the unit tests
	go test -race ./...

lint: ## Run the linters and check formatting
	go tool golangci-lint run ./...
	@test -z "$$(go tool gofumpt -extra -l .)" || (echo "Run 'make format' for:"; go tool gofumpt -extra -l .; exit 1)

vulncheck: ## Check for known vulnerabilities
	go tool govulncheck ./...

format: ## Format the code
	go tool gofumpt -extra -w .

build: ## Build the binary into bin/
	CGO_ENABLED=0 GOFLAGS=-trimpath go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/$(CMD)

run: build ## Build and run, for example: make run ARGS="folders"
	./$(BIN) $(ARGS)

clean: ## Remove build output
	rm -rf bin
