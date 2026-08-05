// Package logical implements the Logical IR of the IR-driven intent
// compiler. It is the framework- and technology-agnostic representation of
// WHAT the user asked for, produced by the Planner before any framework is
// chosen and before any file layout is decided.
//
// A LogicalPlan is a collection of IR nodes plus the relationships between
// them. Every node describes intent in domain terms — a page, a section, a
// component, an API endpoint, a database migration — and MUST NOT reference
// file extensions, directory layouts or framework conventions. The exact
// same node value renders through every framework adapter: passing a
// CreatePageNode{Name: "About"} to the Next.js adapter yields
// app/about/page.tsx, while the Astro adapter renders src/pages/about.astro
// from the identical node.
//
// The parent package (pkg/engine/ir) hosts the adaptive control system's
// execution IR (ExecutionGraph/ExecutionSnapshot). The two IRs are strict:
// the control IR schedules physical work, this Logical IR expresses domain
// intent. Neither imports the other.
package logical
