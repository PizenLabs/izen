BINARY_NAME=izen
VERSION=0.2.0-rmah-wired
BUILD_DIR=bin

.PHONY: all build install test lint clean

all: build

build:
	@echo "Building $(BINARY_NAME) v$(VERSION)..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/izen

install:
	@echo "Installing $(BINARY_NAME) v$(VERSION) globally..."
	go install -ldflags "-X main.Version=$(VERSION)" ./cmd/izen
	@echo "Installation complete! Ensure '$(shell go env GOPATH)/bin' is in your PATH."

test:
	@echo "Running test suite (race, count=1)..."
	go test -race -count=1 ./...

lint:
	@echo "Running golangci-lint (timeout 5m)..."
	golangci-lint run --timeout=5m ./...

clean:
	@echo "Cleaning up..."
	@rm -rf $(BUILD_DIR)
	go clean ./...
	@echo "Clean complete."
