# Security Policy

## Supported Versions

We provide security updates for the following versions:

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | ✅ Yes             |
| < 0.1   | ❌ No              |

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please report them via email to **security@pizenlabs.com**.

Include the following information:
- Type of issue (e.g., buffer overflow, injection, privilege escalation)
- Full paths of source file(s) related to the issue
- Location of the affected source code (tag/branch/commit or URL)
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if available)
- Impact of the issue (what an attacker could achieve)

### Response Timeline

- **Acknowledgment**: Within 48 hours
- **Initial assessment**: Within 5 business days
- **Fix timeline**: Communicated after assessment
- **Disclosure**: Coordinated with reporter

## Security Model

Izen is built with security as a foundational principle:

### Local-First Architecture
- No telemetry, no phone-home, no hidden network calls
- All AI inference via explicitly configured providers
- No data leaves your machine without explicit action

### Execution Sandbox
- **Sandbox mode** (default): Blocks destructive commands (`rm -rf`, `dd`, `mkfs`, etc.)
- **Confirmation required**: All shell commands require explicit user approval
- **Command allowlist**: Configurable permitted commands
- **Timeout enforcement**: All commands have hard timeouts

### Capability Boundaries
| Capability | Default | Scope | Revocable |
|------------|---------|-------|-----------|
| File read | ✅ | Workspace | Yes |
| File write | ❌ (build mode) | Workspace | Yes |
| Shell exec | ❌ (build/investigate) | Configurable | Yes |
| Network | ❌ | Provider config only | Yes |
| Git operations | ✅ | Workspace | Yes |
| MCP servers | ❌ | Explicit enable | Yes |

### Patch & Rollback System
- All mutations captured as unified diffs
- Git checkpoints before every write operation
- `undo` command reverts last patch
- Audit log at `.izen/history.md`

### Threat Model

| Threat | Mitigation |
|--------|------------|
| Malicious AI output | Sandbox validation, confirmations, patch review |
| Supply chain | Vendored dependencies, `go.sum`, Cargo.lock pinned |
| Path traversal | Workspace-root validation, symlink resolution |
| Command injection | Shell=false, argument arrays, allowlist |
| Data exfiltration | Local-first, no background uploads |
| MCP server compromise | Explicit enable, schema validation, stdio isolation |

### Secure Development Practices

- All dependencies pinned and verified (`go mod verify`, `cargo audit`)
- CI runs `govulncheck`, `cargo audit`, `gosec`, `cargo clippy -D warnings`
- No `unsafe` in Go code; minimal `unsafe` in Rust (FFI boundaries only)
- Fuzzing on parsers (Tree-sitter queries, YAML config)
- Property-based testing on graph operations

## Disclosure Policy

We follow **coordinated vulnerability disclosure**:

1. Reporter contacts security@pizenlabs.com
2. We acknowledge within 48 hours
3. We investigate and develop fix
4. We coordinate release timeline with reporter
5. Public advisory published after fix is available

## Security Hall of Fame

We recognize researchers who responsibly disclose vulnerabilities:

*(None yet — be the first!)*

---

**PGP Key**: Available at [keys.pizenlabs.com](https://keys.pizenlabs.com) or via WKD at `security@pizenlabs.com`.
