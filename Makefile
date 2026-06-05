.PHONY: build test lint check run clean tidy

BIN := flock
PKG := ./cmd/flock
GOFLAGS ?= -trimpath

build:
	go build $(GOFLAGS) -o $(BIN) $(PKG)

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy

check: lint test build

run: build
	./$(BIN) up

clean:
	rm -f $(BIN)
	rm -rf data/ .flock/

.DEFAULT_GOAL := build
