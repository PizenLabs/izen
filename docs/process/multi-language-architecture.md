# Multi-Language Architecture — Implementation Progress

## Overview

Implemented intelligent project type auto-detection and multi-language support. Izen now auto-detects the project language/framework on startup and customizes its behavior accordingly.

## What Was Built

### 1. `internal/language/` — Language Registry

Central registry of all supported languages with metadata:

| Feature | Details |
|---------|---------|
| **Languages** | Go, Python, Rust, TypeScript, TSX, JavaScript, Java, Kotlin, C#, C++, C, Ruby, PHP, Swift, SQL, Scala, Elixir, Lua, Shell, Protobuf, YAML, TOML, HTML, CSS (24 total) |
| **Per-language metadata** | File extensions, indicator files, comment syntax, tree-sitter grammar mapping, verification commands (fmt/lint/vet/build/test) |
| **Thread-safe registry** | Concurrent R/W safe, lazy-init singleton, extension/indicator-file/name lookups |

### 2. `internal/project/` — Project Detector

Auto-detects project type by scanning root directory:

- Indicator file matching (go.mod → Go, Cargo.toml → Rust, package.json → JS/TS, pom.xml → Java, etc.)
- Extension-based scoring with deduplication
- Weighted classification with confidence percentage
- Framework detection ready (React, Vue, Django, Spring, etc. — structure in place)

### 3. `internal/graph/` — Dynamic Grammar Loading

24 Tree-sitter grammars registered at init time; parser now:

- Lazily instantiates parsers via `ensureParser()` (grammar-factory pattern)
- Supports Java, C#, Ruby, PHP, Swift, Scala, Kotlin import extraction
- Generic symbol extractors for JS/TS/Java/Kotlin/C#/C++/PHP/Swift

### 4. `internal/execution/` — Language-Aware Verification

- `NewLanguageVerifier(langID)` generates verification steps from registry
- `SetLanguage()` hot-swaps steps mid-session
- Policy capabilities generalized (`CapBuild`, `CapTest`, `CapFmt`, `CapLint`)

### 5. UI Integration

- Language badge shown in status bar (teal colored, e.g. `Go`)
- Language badge shown in startup banner
- Detection result displayed on stderr at startup

## Detection Flow

```
izen start → Detect(root) → scan root files
  ├── go.mod found        → Primary: Go (confidence: 100%)
  ├── package.json found  → Primary: JavaScript (90%), Secondary: TOML
  ├── Cargo.toml found    → Primary: Rust (100%)
  ├── pom.xml found       → Primary: Java (90%)
  └── nothing found       → Warning: could not detect project type
       ↓
execution.Engine created with detected language
       ↓
Verifier configured with language-specific fmt/lint/build/test
       ↓
Language badge shown in TUI status bar + startup banner
```

## Files Changed

| File | Change |
|------|--------|
| `internal/language/types.go` | New — Core types (Def, Verifier, Detected, Category) |
| `internal/language/registry.go` | New — Thread-safe registry with lookups |
| `internal/language/defs.go` | New — 24 language definitions |
| `internal/language/registry_test.go` | New — Tests for registry |
| `internal/project/detector.go` | New — Project detection + scoring |
| `internal/project/detector_test.go` | New — Detection tests |
| `internal/graph/types.go` | Refactored — Language type aliased to registry |
| `internal/graph/parser.go` | Refactored — Dynamic grammar loading, new language extractors |
| `internal/graph/grammars.go` | New — 24 grammar imports + registration |
| `internal/graph/scanner.go` | Refactored — Extended exclude dirs for multi-lang |
| `internal/execution/verify.go` | Refactored — Language-aware verification steps |
| `internal/execution/policy.go` | Refactored — Generic capabilities (CapBuild/CapTest) |
| `internal/execution/execution.go` | Refactored — NewEngine accepts language.ID |
| `internal/config/local.go` | Extended — DetectedLang, DetectedFw fields |
| `cmd/izen/main.go` | Extended — Auto-detection at startup |
| `internal/ui/program.go` | Extended — Detection passed through boot flow |
| `internal/ui/model.go` | Extended — detection field on model |
| `internal/ui/view.go` | Extended — Language badge in status bar + banner |
| `internal/ui/styles.go` | Extended — langBadgeStyle |

## Available Tree-Sitter Grammars

bash, c, cpp, csharp, css, elixir, golang, html, java, javascript, kotlin, lua, php, protobuf, python, ruby, rust, scala, sql, swift, toml, typescript, tsx, yaml

## Next Steps

- Add per-language AST symbol extractors for Java, Ruby, PHP, Swift, Kotlin, C#
- Framework detection from dependency files (package.json deps, Cargo.toml deps, etc.)
- LSP-based type inference fallback for languages without Tree-sitter grammars
