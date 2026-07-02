BINARY_NAME=izen
VERSION=0.1.0
BUILD_DIR=bin
LYNX_DIR=lynx
LYNX_EMBED_DIR=internal/lynx/bin

.PHONY: all build build-lynx build-no-lynx install test test-no-lynx clean

all: build

build: build-lynx
	@echo "Building $(BINARY_NAME) v$(VERSION) with embedded Lynx..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.Version=$(VERSION)" -tags lynx_embed -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/izen

build-lynx:
	@echo "Building Lynx (Rust) in release mode..."
	cd $(LYNX_DIR) && cargo build --release
	@mkdir -p $(LYNX_EMBED_DIR)
	cp $(LYNX_DIR)/target/release/lx $(LYNX_EMBED_DIR)/lx
	@echo "Lynx binary successfully staged inside $(LYNX_EMBED_DIR)/lx for Go embedding."

build-no-lynx:
	@echo "Building $(BINARY_NAME) v$(VERSION) without embedded Lynx (Fallback mode)..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/izen

install: build-lynx
	@echo "Installing $(BINARY_NAME) v$(VERSION) globally via Go infrastructure..."
	go install -ldflags "-X main.Version=$(VERSION)" -tags lynx_embed ./cmd/izen
	@echo "Installation complete! Ensure '$(shell go env GOPATH)/bin' is in your PATH."

test: build-lynx
	@echo "Running full test suite with embedded Lynx..."
	go test -tags lynx_embed ./...

test-no-lynx:
	@echo "Running localized test suite without Lynx tags..."
	go test ./...

clean:
	@echo "Cleaning up local Go build artifacts..."
	@rm -rf $(BUILD_DIR)
	@rm -f $(LYNX_EMBED_DIR)/lx
	go clean ./...
	@echo "Cleaning up Rust Cargo build target cache..."
	cd $(LYNX_DIR) && cargo clean
	@echo "Clean complete."
