BINARY := computah
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/davasorus/computah/cmd.version=$(VERSION)

.PHONY: build install test lint fmt vet clean run

build: ## Build the computah binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

install: ## Install computah to GOPATH/bin
	go install -ldflags "$(LDFLAGS)" .

test: ## Run tests with race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format code
	gofmt -w .
	goimports -w -local github.com/davasorus/computah .

vet: ## Run go vet
	go vet ./...

clean: ## Remove build artifacts
	rm -f $(BINARY)

run: build ## Build and run
	./$(BINARY) run
