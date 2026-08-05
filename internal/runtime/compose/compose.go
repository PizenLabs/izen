// Package compose is the Application-layer composition helper (RFC v1.0
// section 2). It wires the domain WorkflowRuntime, the projection
// LedgerBuilder, the EventTranslator, and the thin Runtime facade into one
// ready-to-use Application, registering every canonical command handler.
//
// It is the only package that both imports the runtime package and its
// handlers (avoiding an import cycle); the composition root (cmd/izen) calls
// Wire and injects the resulting Application into the presentation layer.
package compose

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/events/audit"
	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/handlers"
)

// Capabilities bundles the Infrastructure adapters as Domain ports. The
// composition root instantiates the concrete adapters (OSFile, ExecShell,
// GitCLI, PatchAdapter) and injects them here so the Application layer never
// depends on concrete infrastructure.
type Capabilities struct {
	File  ports.FilePort
	Shell ports.ShellPort
	Git   ports.GitPort
	Patch ports.PatchPort
}

// Application is the fully wired Application layer of the system: the domain
// WorkflowRuntime, the ContextLedger projection, the LedgerBuilder, the
// append-only event audit logger, and the Runtime facade with every command
// handler registered.
type Application struct {
	Bus      *events.Bus
	Workflow workflow.WorkflowRuntime
	Ledger   *runtime.ContextLedger
	Builder  *runtime.LedgerBuilder
	Runtime  *runtime.Runtime
	Audit    *audit.AuditLogger

	// auditDir is the workspace-relative audit log directory wired via
	// WithAuditDir. Empty disables auditing.
	auditDir string

	// Approver resolves patch approvals for the approval command handlers.
	// Defaults to handlers.NewInMemoryApprover when not supplied.
	Approver handlers.PatchApprover
	// Capabilities carries the injected Infrastructure adapters (read-only
	// record for the composition root; not consumed by handlers).
	Capabilities Capabilities
}

// Option configures the Application during wiring.
type Option func(*Application)

// WithBus overrides the shared domain event bus. A nil bus (or no option)
// creates a fresh one.
func WithBus(bus *events.Bus) Option {
	return func(a *Application) {
		if bus != nil {
			a.Bus = bus
		}
	}
}

// WithCapabilities injects the Infrastructure adapters as domain ports.
func WithCapabilities(caps Capabilities) Option {
	return func(a *Application) {
		a.Capabilities = caps
	}
}

// WithApprover injects a custom PatchApprover for the approval handlers.
func WithApprover(approver handlers.PatchApprover) Option {
	return func(a *Application) {
		if approver != nil {
			a.Approver = approver
		}
	}
}

// WithAuditDir wires an append-only event audit logger rooted at dir. Every
// events.Envelope published on the shared bus is appended to
// dir/events.ndjson. An empty dir disables auditing (the Application.Audit
// field stays nil).
func WithAuditDir(dir string) Option {
	return func(a *Application) {
		if dir != "" {
			a.auditDir = dir
		}
	}
}

// Wire builds the Application: domain runtime, dispatcher, handlers, ledger
// projection, and the Runtime facade bound to the shared bus.
func Wire(opts ...Option) (*Application, error) {
	a := &Application{}
	for _, opt := range opts {
		opt(a)
	}
	if a.Bus == nil {
		a.Bus = events.NewBus(events.DefaultBufferSize)
	}
	if a.Approver == nil {
		a.Approver = handlers.NewInMemoryApprover()
	}

	// ── APPEND-ONLY EVENT AUDIT ────────────────────────────────────────
	// The audit logger subscribes to the shared bus and persists every
	// events.Envelope (bridged telemetry, canonical signals) to
	// <auditDir>/events.ndjson asynchronously. It is a pure projection:
	// non-blocking end to end, so disk I/O never stalls the pipeline or the
	// TUI.
	if a.auditDir != "" {
		logger, err := audit.NewLogger(a.auditDir, a.Bus)
		if err != nil {
			return nil, fmt.Errorf("wire: audit logger: %w", err)
		}
		if err := logger.Start(); err != nil {
			return nil, fmt.Errorf("wire: start audit logger: %w", err)
		}
		a.Audit = logger
	}

	wf := workflow.NewWorkflowRuntime()
	a.Workflow = wf

	dispatcher := runtime.NewCommandDispatcher()
	hs := handlers.New(handlers.HandlerDeps{
		Workflow: wf,
		Bus:      a.Bus,
		Approver: a.Approver,
	})
	if err := hs.Register(dispatcher); err != nil {
		return nil, err
	}

	builder := runtime.NewLedgerBuilder(a.Bus)
	builder.Start()
	a.Builder = builder
	a.Ledger = builder.Ledger()

	a.Runtime = runtime.NewRuntime(dispatcher, runtime.WithEventBus(a.Bus))
	return a, nil
}

// Close tears down the Application: it stops the audit logger, the ledger
// projection and the runtime presentation projection. Idempotent.
func (a *Application) Close() {
	if a == nil {
		return
	}
	if a.Audit != nil {
		_ = a.Audit.Close()
		a.Audit = nil
	}
	if a.Builder != nil {
		a.Builder.Close()
	}
	if a.Runtime != nil {
		a.Runtime.Close()
	}
}
