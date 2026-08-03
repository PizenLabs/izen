package graph

// NodeKind classifies a node in the structural graph.
type NodeKind string

const (
	// KindFile is a source file.
	KindFile NodeKind = "file"
	// KindPackage is a package or module directory.
	KindPackage NodeKind = "package"
	// KindFunction is a free function.
	KindFunction NodeKind = "function"
	// KindMethod is a method bound to a receiver/type.
	KindMethod NodeKind = "method"
	// KindStruct is a struct/class/record type.
	KindStruct NodeKind = "struct"
	// KindClass is a class in OO languages.
	KindClass NodeKind = "class"
	// KindInterface is an interface/trait type.
	KindInterface NodeKind = "interface"
	// KindType is a generic type declaration.
	KindType NodeKind = "type"
	// KindEnum is an enum type.
	KindEnum NodeKind = "enum"
	// KindHTTPRoute is an HTTP endpoint.
	KindHTTPRoute NodeKind = "http_route"
)

// EdgeKind classifies an edge in the structural graph.
type EdgeKind string

const (
	// EdgeCalls links a caller function to a callee function.
	EdgeCalls EdgeKind = "CALLS"
	// EdgeImports links a file to a package it imports.
	EdgeImports EdgeKind = "IMPORTS"
	// EdgeDefines links a package/file to the symbols it declares.
	EdgeDefines EdgeKind = "DEFINES"
	// EdgeImplements links a concrete type to an interface it satisfies.
	EdgeImplements EdgeKind = "IMPLEMENTS"
	// EdgeHTTPHandles links an HTTP route to its handler function.
	EdgeHTTPHandles EdgeKind = "HTTP_HANDLES"
)

// Node is a single entity in the structural graph. IDs are stable across
// incremental rebuilds so that call edges survive file edits.
type Node struct {
	ID        string
	Kind      NodeKind
	Name      string
	QualName  string
	Package   string
	File      string
	Line      int
	EndLine   int
	Exported  bool
	Signature string
	Methods   []string
}

// Edge connects two nodes. From/To hold node IDs.
type Edge struct {
	From string
	To   string
	Kind EdgeKind
	Line int
}
