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
	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
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
// WorkflowRuntime, the ContextLedger projection, the LedgerBuilder, and the
// Runtime facade with every command handler registered.
type Application struct {
	Bus      *events.Bus
	Workflow workflow.WorkflowRuntime
	Ledger   *runtime.ContextLedger
	Builder  *runtime.LedgerBuilder
	Runtime  *runtime.Runtime

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

// Close tears down the Application: it stops the ledger projection and the
// runtime presentation projection. Idempotent.
func (a *Application) Close() {
	if a == nil {
		return
	}
	if a.Builder != nil {
		a.Builder.Close()
	}
	if a.Runtime != nil {
		a.Runtime.Close()
	}
}
