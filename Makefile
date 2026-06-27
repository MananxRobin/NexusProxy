BINARY_NAME ?= nexusproxy
BUILD_DIR ?= dist
GO ?= $(shell command -v go 2>/dev/null || printf "%s/.local/go/bin/go" "$$HOME")
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf "dev")
PLATFORMS ?= darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: build clean install release run test

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/nexusproxy

clean:
	rm -rf $(BUILD_DIR)

install:
	./scripts/install.sh

release:
	VERSION=$(VERSION) OUT_DIR=$(BUILD_DIR) PLATFORMS="$(PLATFORMS)" ./scripts/package-release.sh

run:
	$(GO) run ./cmd/nexusproxy --config config.example.json

test:
	$(GO) test ./...
