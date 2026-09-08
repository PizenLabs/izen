// Package layer2 implements the Context Policy Engine of the Izen engine.
//
// It sits on top of the lea System of Record (SoR) and materializes an
// immutable ExecutionContext for a given request. Context is governed by
// policy, never accumulated: a ContextGovernor combines a structural ranker
// and an AST-aware compressor to assemble exactly the files and symbols a
// target needs, then strictly enforces the policy token budget before the
// context is returned.
//
// The layer is split into four cooperating components:
//
//	Sor        - thread-safe facade over lea.Engine (ASTs, call graphs,
//	             symbol tables, imports, file relationships).
//	Ranker     - personalized PageRank over the SoR call graph, biased by
//	             call-graph depth to the request target.
//	Compressor - AST-aware body stripping that preserves signatures, type
//	             definitions, interfaces and doc comments.
//	ContextGovernor - orchestrates ranking + compression and enforces the
//	             ContextPolicy token budget strictly.
//
// Every value returned by this package is immutable: construction performs
// deep copies and returned collections never alias the SoR's internal state.
package layer2
