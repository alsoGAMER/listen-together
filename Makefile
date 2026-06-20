BINARY := listen-together
PKG    := ./cmd/listen-together
PORT   ?= 4040

.PHONY: build run test race vet fmt lint tidy docker clean

build: ## Build the binary
	go build -ldflags="-s -w" -o $(BINARY) $(PKG)

run: ## Run locally (LT_PORT, LT_ALLOWED_SERVERS honored)
	LT_PORT=$(PORT) go run $(PKG)

test: ## Run tests
	go test ./... -count=1

race: ## Run tests with the race detector
	go test ./... -count=1 -race

vet: ## go vet
	go vet ./...

fmt: ## Format
	gofmt -w .

lint: vet ## Format check + vet
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

tidy: ## Tidy modules
	go mod tidy

docker: ## Build the container image
	docker build -t $(BINARY):latest .

clean:
	rm -f $(BINARY)

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'
