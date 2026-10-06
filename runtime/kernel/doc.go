// Package kernel is the IZEN Runtime Kernel: the single authoritative
// substrate that decides what executes, whether it may execute, what happened,
// and what is therefore true.
//
// # Constitutional principles
//
// The kernel implements four rules and refuses to bend any of them:
//
//	LLMs reason.      A proposal is DATA. It carries no authority.
//	The Engine decides. This package decides admission, authorization,
//	                   invocation order, and terminal truth.
//	Capabilities execute. A capability does one observable thing and reports
//	                   what it actually observed.
//	Humans remain in control. A grant is an explicit, inspectable decision.
//
// And the governing law, from which everything else follows:
//
//	Dynamic Path, Static Authority, Truthful State Transition.
//
// # The acceptance invariant
//
//	proposal != authorization != execution != evidence
//	         != verification != objective completion
//
// Concretely, the kernel enforces:
//
//	No authorization  -> no capability is invoked
//	No capability     -> no invocation is fabricated
//	No evidence       -> no state advances past the observation
//	No verification   -> no PROVEN
//	No evidence       -> no PROVEN
//
// # The four separate axes
//
// A completion claim collapses four independent facts if they are merged, and
// each merge is a lie:
//
//	Provider DONE      the socket closed          (transport fact)
//	Artifact PRODUCED  a parser extracted it      (syntax fact)
//	Mutation APPLIED   the filesystem changed     (state fact)
//	Objective PROVEN   the contract is satisfied  (meaning fact)
//
// The kernel keeps them as four separate types (ProviderAxis, ArtifactAxis,
// MutationAxis, VerifyAxis, Outcome) and only Outcome may authorize completion.
// A provider that stops emitting tokens satisfies none of the others, so it
// structurally cannot reach PROVEN.
//
// # Determinism
//
// State is a pure fold over events. Apply(State, Event) -> State is total,
// side-effect free, and depends on nothing but its arguments. Every fact in the
// state was produced by a capability that ran, or it is absent. There is no
// heuristic default, no inferred progress, and no ambient state: the same event
// sequence always yields the same state, on any machine.
//
// # Authority boundary
//
// This package imports only the Go standard library. It has no dependency on a
// terminal, a renderer, a provider, a configuration file, or any IZEN package.
// It is usable from a headless process, a test, or another Go program. A UI
// observes the kernel; the kernel never observes the UI.
//
// # Layout
//
//	spec.go          ExecutionSpec, Step, Program, Contract
//	capability.go    the Capability boundary and its registry
//	authorization.go Grant and the authorization gate
//	budget.go        budget allocation and accounting
//	evidence.go      Evidence, Observation, Verdict
//	event.go         the closed Event taxonomy and the durable log
//	state.go         the single authoritative State and its four axes
//	failure.go       the closed failure taxonomy
//	outcome.go       evidence-gated terminal truth
//	reduce.go        the pure reducer
//	engine.go        the execution lifecycle: admit, run, settle
//	dispatch.go      the ordered path from "a step is next" to "evidence exists"
//	result.go        the terminal Result projection
package kernel
