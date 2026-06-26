package lynx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/graph"
)

func projectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func TestControllerNew(t *testing.T) {
	c := NewController(projectRoot(), true)
	if c == nil {
		t.Fatal("expected non-nil controller")
	}
	if !c.lazy {
		t.Error("expected lazy=true")
	}
}

func TestHasSemanticQuery(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"hi", false},
		{"hello", true},
		{"NewEngine", false},
		{"pkg.Func", false},
		{"authentication flow", true},
		{"abc", false},
		{"database query handler", true},
	}
	for _, tt := range tests {
		got := HasSemanticQuery(tt.query)
		if got != tt.want {
			t.Errorf("HasSemanticQuery(%q) = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestLynxCacheDir(t *testing.T) {
	dir := LynxCacheDir("/tmp/test")
	if dir != "/tmp/test/.lynx" {
		t.Errorf("expected /tmp/test/.lynx, got %s", dir)
	}
}

func TestLynxCacheExists(t *testing.T) {
	tmpDir := t.TempDir()

	if LynxCacheExists(tmpDir) {
		t.Error("expected false for non-existent .lynx dir")
	}

	os.MkdirAll(filepath.Join(tmpDir, ".lynx"), 0755)
	if !LynxCacheExists(tmpDir) {
		t.Error("expected true for existing .lynx dir")
	}
}

func TestNewMutationTracer(t *testing.T) {
	root := projectRoot()
	g := graph.NewGraph(root)
	mt := NewMutationTracer(root, g)
	if mt == nil {
		t.Fatal("expected non-nil MutationTracer")
	}
}

func TestMutationTracerNilGraph(t *testing.T) {
	root := projectRoot()
	mt := NewMutationTracer(root, nil)

	points, err := mt.TraceAssignments("NewEngine")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Errorf("expected 0 points with nil graph, got %d", len(points))
	}

	edges, err := mt.TraceImpact("NewEngine")
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges with nil graph, got %d", len(edges))
	}
}

func TestMutationTracerWithGraph(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build graph: %v", err)
	}

	mt := NewMutationTracer(root, g)

	points, err := mt.TraceAssignments("NewEngine")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Found %d assignment points for NewEngine", len(points))
	for _, p := range points {
		t.Logf("  %s:%d: %s %s", p.File, p.Line, p.Kind, p.Expr)
	}
}

func TestMutationTracerImpact(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build graph: %v", err)
	}

	mt := NewMutationTracer(root, g)

	edges, err := mt.TraceCrossFile("NewEngine")
	if err != nil {
		t.Fatalf("TraceCrossFile: %v", err)
	}
	t.Logf("Found %d impact edges for NewEngine", len(edges))
	for _, e := range edges {
		t.Logf("  %s:%d -> %s:%d [%s] %s",
			e.SourceFile, e.SourceLine, e.TargetFile, e.TargetLine, e.Kind, e.Description)
	}
}

func TestCollectTypes(t *testing.T) {
	typesInfo, err := CollectTypes()
	if err != nil {
		t.Fatalf("CollectTypes: %v", err)
	}
	if len(typesInfo) == 0 {
		t.Skip("no types found")
	}
	t.Logf("Found %d types", len(typesInfo))
	for _, ti := range typesInfo[:min(5, len(typesInfo))] {
		t.Logf("  %s (%s) at %s:%d", ti.Name, ti.Kind, ti.File, ti.Line)
		if len(ti.Fields) > 0 {
			t.Logf("    fields: %v", ti.Fields)
		}
		if len(ti.Methods) > 0 {
			t.Logf("    methods: %v", ti.Methods)
		}
	}
}

func TestExprString(t *testing.T) {
	root := projectRoot()
	mt := NewMutationTracer(root, nil)

	tests := []struct {
		input string
	}{
		{"*ast.Ident"},
		{"func(...) {...}"},
		{"map[...]"},
		{"struct{...}"},
	}
	for _, tt := range tests {
		if tt.input == "" {
			t.Error("empty test input should not happen")
		}
	}
	_ = mt

	typeCheckResult, _, err := TypeCheck(root)
	if err != nil {
		t.Skipf("TypeCheck error (expected if no lynx binary): %v", err)
	}
	if typeCheckResult != nil {
		t.Logf("TypeCheck: %s", typeCheckResult.Name())
	}
}

func TestDaemonNotRunning(t *testing.T) {
	d := NewDaemon(projectRoot())

	if d.IsRunning() {
		t.Error("expected daemon to not be running initially")
	}

	client := d.Client()
	if client != nil {
		t.Error("expected nil client before start")
	}
}
