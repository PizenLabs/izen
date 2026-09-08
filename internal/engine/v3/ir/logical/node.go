package logical

import (
	"fmt"
	"strings"
)

// NodeKind discriminates one Logical IR node type.
type NodeKind string

// Logical IR node kinds. These are the ONLY node kinds the compiler
// understands; adding a kind requires an adapter mapping in the lowerer.
const (
	// NodeCreatePage creates one navigable page (e.g. an "About" page).
	NodeCreatePage NodeKind = "create_page"
	// NodeCreateSection creates one content section inside a page.
	NodeCreateSection NodeKind = "create_section"
	// NodeCreateComponent creates one reusable UI component.
	NodeCreateComponent NodeKind = "create_component"
	// NodeDefineEndpoint defines one API endpoint.
	NodeDefineEndpoint NodeKind = "define_endpoint"
	// NodeCreateMigration creates one database migration.
	NodeCreateMigration NodeKind = "create_migration"
	// NodeCreateStyle creates the styling of the site (a stylesheet).
	NodeCreateStyle NodeKind = "create_style"
	// NodeCreateScript creates the behaviour of the site (a script).
	NodeCreateScript NodeKind = "create_script"
)

// Valid reports whether k is one of the defined Logical IR node kinds.
func (k NodeKind) Valid() bool {
	switch k {
	case NodeCreatePage, NodeCreateSection, NodeCreateComponent,
		NodeDefineEndpoint, NodeCreateMigration, NodeCreateStyle, NodeCreateScript:
		return true
	default:
		return false
	}
}

// String returns the machine-readable kind label.
func (k NodeKind) String() string { return string(k) }

// IRNode is one domain-agnostic node of a LogicalPlan. It expresses WHAT the
// plan does in domain terms and carries zero physical detail: no file
// extensions, no path layouts, no framework conventions. Implementations are
// immutable after construction and must never be mutated by adapters.
//
// Accessors are prefixed (NodeID/NodeName/NodeDesc) so they coexist with the
// exported value fields (ID/Name/Description) the constructors and adapters
// read directly.
type IRNode interface {
	// Kind classifies the node.
	Kind() NodeKind
	// NodeID uniquely identifies the node within its LogicalPlan.
	NodeID() string
	// NodeName is the user-facing node name (e.g. "About").
	NodeName() string
	// NodeDesc documents the node's purpose.
	NodeDesc() string
}

// ─── CreatePageNode ─────────────────────────────────────────────────────────

// CreatePageNode expresses intent to create one navigable page. It is purely
// logical: the physical file location (app/about/page.tsx vs
// src/pages/about.astro) is decided by the framework adapter, never here.
type CreatePageNode struct {
	// ID uniquely identifies the node. Empty falls back to "page-<name>".
	ID string
	// Name is the page name, e.g. "About".
	Name string
	// Title is the page heading. Empty falls back to Name.
	Title string
	// Sections lists the section names composed into this page.
	Sections []string
	// Description documents the page's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreatePageNode) Kind() NodeKind { return NodeCreatePage }

// NodeID implements IRNode.
func (n *CreatePageNode) NodeID() string { return nodeID("page", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreatePageNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreatePageNode) NodeDesc() string { return n.Description }

// PageTitle returns the rendered page heading, falling back to the name.
func (n *CreatePageNode) PageTitle() string {
	if strings.TrimSpace(n.Title) != "" {
		return n.Title
	}
	return n.Name
}

// SectionNames returns a defensive copy of the composed section names.
func (n *CreatePageNode) SectionNames() []string {
	return append([]string(nil), n.Sections...)
}

// clone returns a deep copy of the node so plans never share mutable
// references with their callers.
func (n *CreatePageNode) clone() IRNode {
	c := *n
	c.Sections = append([]string(nil), n.Sections...)
	return &c
}

// ─── CreateSectionNode ──────────────────────────────────────────────────────

// CreateSectionNode expresses intent to create one content section.
type CreateSectionNode struct {
	// ID uniquely identifies the node. Empty falls back to "section-<name>".
	ID string
	// Name is the section name, e.g. "Hero".
	Name string
	// Content is the section's natural-language content description.
	Content string
	// Description documents the section's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreateSectionNode) Kind() NodeKind { return NodeCreateSection }

// NodeID implements IRNode.
func (n *CreateSectionNode) NodeID() string { return nodeID("section", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreateSectionNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreateSectionNode) NodeDesc() string { return n.Description }

// clone returns a deep copy of the node.
func (n *CreateSectionNode) clone() IRNode {
	c := *n
	return &c
}

// ─── CreateComponentNode ────────────────────────────────────────────────────

// CreateComponentNode expresses intent to create one reusable UI component.
type CreateComponentNode struct {
	// ID uniquely identifies the node. Empty falls back to "component-<name>".
	ID string
	// Name is the component name, e.g. "Navbar".
	Name string
	// Purpose is the component's responsibility in natural language.
	Purpose string
	// DependsOn lists the component node ids this component depends on.
	DependsOn []string
	// Description documents the component's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreateComponentNode) Kind() NodeKind { return NodeCreateComponent }

// NodeID implements IRNode.
func (n *CreateComponentNode) NodeID() string { return nodeID("component", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreateComponentNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreateComponentNode) NodeDesc() string { return n.Description }

// Dependencies returns a defensive copy of the dependency node ids.
func (n *CreateComponentNode) Dependencies() []string {
	return append([]string(nil), n.DependsOn...)
}

// clone returns a deep copy of the node.
func (n *CreateComponentNode) clone() IRNode {
	c := *n
	c.DependsOn = append([]string(nil), n.DependsOn...)
	return &c
}

// ─── CreateEndpointNode ─────────────────────────────────────────────────────

// CreateEndpointNode expresses intent to define one API endpoint. Route is a
// logical URL path (e.g. "/api/users"), never a filesystem path.
type CreateEndpointNode struct {
	// ID uniquely identifies the node. Empty falls back to "endpoint-<name>".
	ID string
	// Method is the HTTP method, e.g. "GET", "POST".
	Method string
	// Route is the logical URL route, e.g. "/api/users".
	Route string
	// Name is the handler name, e.g. "ListUsers".
	Name string
	// Description documents the endpoint's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreateEndpointNode) Kind() NodeKind { return NodeDefineEndpoint }

// NodeID implements IRNode.
func (n *CreateEndpointNode) NodeID() string { return nodeID("endpoint", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreateEndpointNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreateEndpointNode) NodeDesc() string { return n.Description }

// clone returns a deep copy of the node.
func (n *CreateEndpointNode) clone() IRNode {
	c := *n
	return &c
}

// ─── CreateDatabaseMigrationNode ────────────────────────────────────────────

// CreateDatabaseMigrationNode expresses intent to create one database
// migration.
type CreateDatabaseMigrationNode struct {
	// ID uniquely identifies the node. Empty falls back to "migration-<name>".
	ID string
	// Name is the migration name, e.g. "create_users_table".
	Name string
	// Description documents the migration's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreateDatabaseMigrationNode) Kind() NodeKind { return NodeCreateMigration }

// NodeID implements IRNode.
func (n *CreateDatabaseMigrationNode) NodeID() string { return nodeID("migration", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreateDatabaseMigrationNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreateDatabaseMigrationNode) NodeDesc() string { return n.Description }

// clone returns a deep copy of the node.
func (n *CreateDatabaseMigrationNode) clone() IRNode {
	c := *n
	return &c
}

// ─── CreateStyleNode ─────────────────────────────────────────────────────────

// CreateStyleNode expresses intent to create the styling of the site. It is a
// logical node — the physical artifact (styles.css, app/globals.css, ...) is
// decided by the framework adapter.
type CreateStyleNode struct {
	// ID uniquely identifies the node. Empty falls back to "style-<name>".
	ID string
	// Name is the stylesheet name, e.g. "styles".
	Name string
	// Description documents the styling's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreateStyleNode) Kind() NodeKind { return NodeCreateStyle }

// NodeID implements IRNode.
func (n *CreateStyleNode) NodeID() string { return nodeID("style", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreateStyleNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreateStyleNode) NodeDesc() string { return n.Description }

// clone returns a deep copy of the node.
func (n *CreateStyleNode) clone() IRNode {
	c := *n
	return &c
}

// ─── CreateScriptNode ────────────────────────────────────────────────────────

// CreateScriptNode expresses intent to create the behaviour of the site. It is
// a logical node — the physical artifact (script.js, app/.../script.ts, ...)
// is decided by the framework adapter.
type CreateScriptNode struct {
	// ID uniquely identifies the node. Empty falls back to "script-<name>".
	ID string
	// Name is the script name, e.g. "script".
	Name string
	// Behavior describes the intended behaviour in natural language.
	Behavior string
	// Description documents the script's purpose.
	Description string
}

// Kind implements IRNode.
func (n *CreateScriptNode) Kind() NodeKind { return NodeCreateScript }

// NodeID implements IRNode.
func (n *CreateScriptNode) NodeID() string { return nodeID("script", n.ID, n.Name) }

// NodeName implements IRNode.
func (n *CreateScriptNode) NodeName() string { return n.Name }

// NodeDesc implements IRNode.
func (n *CreateScriptNode) NodeDesc() string { return n.Description }

// clone returns a deep copy of the node.
func (n *CreateScriptNode) clone() IRNode {
	c := *n
	return &c
}

// ─── helpers ────────────────────────────────────────────────────────────────

// nodeID derives a deterministic node identifier from the explicit ID or a
// kind/name fallback. Name-derived ids are lowercased and never slugified:
// slugification is a physical concern owned by the adapters.
func nodeID(prefix, explicit, name string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	return fmt.Sprintf("%s-%s", prefix, strings.ToLower(strings.TrimSpace(name)))
}
