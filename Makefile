BINARY_NAME=izen
VERSION=0.1.1
BUILD_DIR=bin

.PHONY: all build install test clean

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
	@echo "Running test suite..."
	go test ./...

clean:
	@echo "Cleaning up..."
	@rm -rf $(BUILD_DIR)
	go clean ./...
	@echo "Clean complete."
