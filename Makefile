BINARY_NAME=izen
VERSION=0.1.0
BUILD_DIR=bin

.PHONY: all build build-lynx install test test-no-lynx clean

all: build

build: build-lynx
	@echo "Building $(BINARY_NAME) v$(VERSION) into local clean directory..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.Version=$(VERSION)" -tags lynx_embed -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/izen

build-lynx:
	@echo "Building Lynx (Rust)..."
	cd lynx && cargo build --release
	@mkdir -p internal/lynx/bin
	cp lynx/target/release/lx internal/lynx/bin/lx
	@echo "Lynx binary copied to internal/lynx/bin/lx"

build-no-lynx:
	@echo "Building $(BINARY_NAME) v$(VERSION) into local clean directory..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/izen

install: build
	@echo "Installing $(BINARY_NAME) globally into /usr/local/bin..."
	sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	@echo "Installation complete! You can now run '$(BINARY_NAME)' natively from anywhere."

test:
	go test -tags lynx_embed ./...

test-no-lynx:
	go test ./...

clean:
	@echo "Cleaning up local build artifacts inside $(BUILD_DIR)..."
	@rm -rf $(BUILD_DIR)
	rm -f internal/lynx/bin/lx
	go clean ./...
