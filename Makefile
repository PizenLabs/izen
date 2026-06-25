.PHONY: build build-lynx test clean

build: build-lynx
	go build -tags lynx_embed ./cmd/izen

build-lynx:
	@echo "Building Lynx (Rust)..."
	cd lynx && cargo build --release
	@mkdir -p internal/lynx/bin
	cp lynx/target/release/lx internal/lynx/bin/lx
	@echo "Lynx binary copied to internal/lynx/bin/lx"

build-no-lynx:
	go build ./cmd/izen

test:
	go test -tags lynx_embed ./...

test-no-lynx:
	go test ./...

clean:
	rm -f internal/lynx/bin/lx
	go clean ./...