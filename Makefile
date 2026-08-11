SHELL := /bin/sh

BINARY := bin/mcp-gate
VERSION ?= 0.1.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/sid-pani/lessen-mcp-gateway/internal/version.Version=$(VERSION) \
	-X github.com/sid-pani/lessen-mcp-gateway/internal/version.Commit=$(COMMIT) \
	-X github.com/sid-pani/lessen-mcp-gateway/internal/version.Date=$(BUILD_DATE)

.PHONY: all build build-postgres test test-race check fmt vet clean run docker

all: check build

build:
	mkdir -p bin
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/mcp-gate

build-postgres:
	mkdir -p bin
	CGO_ENABLED=1 go build -trimpath -tags postgres -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/mcp-gate

test:
	go test ./...

test-race:
	go test -race ./...

fmt:
	@files="$$(gofmt -l $$(find cmd internal web -name '*.go' -type f))"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

vet:
	go vet ./...

check: fmt vet
	go test -race ./...
	go test -tags postgres ./...
	node --check web/app.js

run: build
	./$(BINARY) serve

docker:
	docker build -t lessen-mcp-gateway .

clean:
	rm -rf bin dist coverage.out
