.PHONY: fmt check clippy test ci

fmt:
	cargo fmt --all

check:
	cargo check

clippy:
	cargo clippy --all-targets -- -D warnings

test:
	cargo test

ci: fmt check clippy test
