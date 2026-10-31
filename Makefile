GO ?= $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
BINARY_NAME=bin/relay

.PHONY: all build test clean

all: test build

build:
	CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(BINARY_NAME) ./cmd/relay

test:
	$(GO) test -v ./...

clean:
	rm -rf bin/
