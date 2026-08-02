// Package infrastructure implements the concrete adapters for the Izen
// architecture (RFC v1.0 section 2): filesystem, shell, git, and patch
// capability ports, plus the in-memory event publisher.
//
// Every type in this package is a concrete adapter that satisfies a port
// interface declared in internal/domain/ports (or the shared event contract).
// Dependencies flow inward: infrastructure implements domain interfaces and
// never calls upward into the domain, application, or presentation layers.
package infrastructure
