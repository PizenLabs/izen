// Package layer4 implements the Validation DAG Engine of the Izen engine.
//
// It sits on top of Layer 1 (capability graph), Layer 2 (System of Record)
// and Layer 3 (proposed patches) and owns how a proposed mutation set is
// validated before it is accepted.
//
// Validation follows two architectural rules:
//
//	Cheap First, Expensive Last: low-cost in-RAM checks (structural AST
//	analysis, syntax parsing) always run before high-cost workspace commands
//	(lint, build, test).
//
//	Early Short-Circuit: validation stages are modeled as a Directed Acyclic
//	Graph. A node only starts once every dependency has passed, so a failing
//	structural or syntax check instantly prevents the expensive build and
//	test stages from ever starting.
//
// The layer is split into four cooperating components:
//
//	Resolver           - dynamically resolves a ValidationPlan from the Layer 1
//	                     capability graph; build/test stages are never
//	                     fabricated for workspaces that lack the capability.
//	StructuralValidator - zero-CLI, RAM-only structural analysis of proposed
//	                     patches over the Layer 2 SoR: broken imports, dangling
//	                     references and syntax errors, all in memory.
//	DAG                - topological-sorted, concurrently executed validation
//	                     graph with early short-circuiting.
//	Validators         - concrete implementations (structural, syntax, lint,
//	                     build, test) returning a structured ValidationResult
//	                     carrying error location, stdout/stderr and exit status.
//
// Everything is safe for concurrent use; the DAG engine is race-free under
// the race detector, and all failure paths are reported through sentinel
// errors reachable with errors.Is.
package layer4
