package build

import (
	"context"
	"fmt"
	"strings"
	"sync"

	izenctx "github.com/PizenLabs/izen/internal/context"
)

type BuildRecoveryState int

const (
	RecoveryNone BuildRecoveryState = iota
	RecoveryFirstPatch
	RecoveryForceRewrite
)

type Engine struct {
	recoveryCount int
	recoveryState BuildRecoveryState
	files         map[string]int
	maxRecovery   int

	// ledger bridges /build execution to the /plan checklist state.
	ledger *izenctx.TaskLedger
	// contextID scopes mutations for the guardrail audit log.
	contextID string

	mu          sync.Mutex
	applied     int
	lastSummary string
}

func NewEngine() *Engine {
	return &Engine{
		recoveryCount: 0,
		recoveryState: RecoveryNone,
		files:         make(map[string]int),
		maxRecovery:   1,
	}
}

func (e *Engine) RecoveryCount() int {
	return e.recoveryCount
}

func (e *Engine) RecoveryState() BuildRecoveryState {
	return e.recoveryState
}

func (e *Engine) RecordCompilationFailure(file string) {
	e.recoveryCount++
	e.files[file]++
	if e.recoveryCount > e.maxRecovery {
		e.recoveryState = RecoveryForceRewrite
	}
}

func (e *Engine) RecordCompilationSuccess() {
	e.recoveryCount = 0
	e.recoveryState = RecoveryNone
}

func (e *Engine) FileFailureCount(file string) int {
	return e.files[file]
}

func (e *Engine) MustRewriteEntireFile(file string) bool {
	return e.recoveryState == RecoveryForceRewrite || e.files[file] > e.maxRecovery
}

func (e *Engine) Reset() {
	e.recoveryCount = 0
	e.recoveryState = RecoveryNone
	e.files = make(map[string]int)
}

func (e *Engine) ValidateFirstToken(output string) error {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return fmt.Errorf("build output is empty")
	}
	if !strings.HasPrefix(trimmed, "```") &&
		!strings.HasPrefix(trimmed, "FILE:") &&
		!strings.HasPrefix(trimmed, "--- a/") {
		return fmt.Errorf("first token must be code fence (```), FILE:, or --- a/, got: %.40s", trimmed)
	}
	return nil
}

func (e *Engine) ExecuteFileMutation(ctx context.Context, file string, content string) error {
	return nil
}
