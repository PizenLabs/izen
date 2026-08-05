// Package adapter implements the pure render adapters of the IR-driven
// intent compiler. An adapter is a PURE RENDERER: it translates one Logical
// IR node into concrete FileArtifacts and contains zero planning, inference
// or policy logic. The exact same IR node renders differently per framework
// — CreatePageNode{Name: "About"} becomes app/about/page.tsx through the
// Next.js adapter and src/pages/about.astro through the Astro adapter.
//
// Adapters never decide WHICH framework to use (that is the inference +
// policy stage) and never decide WHICH nodes to render (that is the plan
// stage). They are selected by the lowerer's CapabilityRegistry mapping of
// (Framework, Capability) → adapter.
package adapter

import (
	"io/fs"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// Framework identifies a concrete rendering framework. The inference engine
// emits these labels; the lowerer resolves them to adapters.
type Framework string

// Supported frameworks.
const (
	// FrameworkNextJS is the Next.js app router framework.
	FrameworkNextJS Framework = "nextjs"
	// FrameworkAstro is the Astro content framework.
	FrameworkAstro Framework = "astro"
	// FrameworkReactVite is a React single-page app on Vite.
	FrameworkReactVite Framework = "react-vite"
	// FrameworkGoGin is a Go HTTP service on the Gin router.
	FrameworkGoGin Framework = "go-gin"
	// FrameworkStaticWeb is a static HTML/CSS/JS site with no build step.
	FrameworkStaticWeb Framework = "static-web"
)

// String returns the machine-readable framework label.
func (f Framework) String() string { return string(f) }

// FrameworkAdapter is a pure renderer that translates Logical IR nodes into
// concrete FileArtifacts. Adapters MUST NOT make planning decisions: given
// the same node they always produce the same artifacts, and they never
// mutate the node they render.
type FrameworkAdapter interface {
	// Framework returns the framework this adapter renders for.
	Framework() Framework
	// RenderNode translates one Logical IR node into concrete file
	// artifacts. An unsupported node kind is an error.
	RenderNode(node ir.IRNode) ([]FileArtifact, error)
}

// FileArtifact is one concrete, rendered file: the framework-specific
// relative path and its full content. It is the only output of an adapter.
type FileArtifact struct {
	// Path is the concrete relative path, e.g. "app/about/page.tsx".
	Path string
	// Content is the full rendered file content.
	Content string
	// Mode is the file mode applied when the artifact is written.
	Mode fs.FileMode
}

// defaultFileMode is applied to rendered artifacts.
const defaultFileMode fs.FileMode = 0o644
