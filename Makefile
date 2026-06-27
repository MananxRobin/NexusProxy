BINARY_NAME ?= nexusproxy
BUILD_DIR ?= dist
GO ?= $(shell command -v go 2>/dev/null || printf "%s/.local/go/bin/go" "$$HOME")

.PHONY: build clean install run test

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/nexusproxy

clean:
	rm -rf $(BUILD_DIR)

install:
	./scripts/install.sh

run:
	$(GO) run ./cmd/nexusproxy --config config.example.json

test:
	$(GO) test ./...
