GO ?= $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
BINARY_NAME=bin/relay

.PHONY: all build test clean install

all: test build

build:
	CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(BINARY_NAME) ./cmd/relay

install: build
	install -d $(HOME)/.local/bin
	install -m 755 $(BINARY_NAME) $(HOME)/.local/bin/relay

test:
	$(GO) test -v ./...

clean:
	rm -rf bin/
