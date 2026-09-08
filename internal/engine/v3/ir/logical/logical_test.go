package logical

import (
	"reflect"
	"testing"
)

func TestCreatePageNodeDefaults(t *testing.T) {
	n := &CreatePageNode{Name: "About"}
	if n.Kind() != NodeCreatePage {
		t.Fatalf("kind = %q, want %q", n.Kind(), NodeCreatePage)
	}
	if n.NodeID() != "page-about" {
		t.Fatalf("id = %q, want %q", n.NodeID(), "page-about")
	}
	if n.NodeName() != "About" {
		t.Fatalf("name = %q, want %q", n.NodeName(), "About")
	}
	if n.PageTitle() != "About" {
		t.Fatalf("title = %q, want fallback to name", n.PageTitle())
	}
}

func TestCreatePageNodeExplicitIDAndTitle(t *testing.T) {
	n := &CreatePageNode{ID: "pg-1", Name: "About", Title: "About Us"}
	if n.NodeID() != "pg-1" {
		t.Fatalf("id = %q, want explicit %q", n.NodeID(), "pg-1")
	}
	if n.PageTitle() != "About Us" {
		t.Fatalf("title = %q, want explicit %q", n.PageTitle(), "About Us")
	}
}

func TestAllNodeKindsReportValidKinds(t *testing.T) {
	nodes := []IRNode{
		&CreatePageNode{Name: "About"},
		&CreateSectionNode{Name: "Hero"},
		&CreateComponentNode{Name: "Navbar"},
		&CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"},
		&CreateDatabaseMigrationNode{Name: "create_users_table"},
		&CreateStyleNode{Name: "styles"},
		&CreateScriptNode{Name: "script"},
	}
	want := []NodeKind{
		NodeCreatePage, NodeCreateSection, NodeCreateComponent,
		NodeDefineEndpoint, NodeCreateMigration, NodeCreateStyle, NodeCreateScript,
	}
	for i, n := range nodes {
		if n.Kind() != want[i] {
			t.Fatalf("node %d kind = %q, want %q", i, n.Kind(), want[i])
		}
	}
}

func TestStyleScriptNodeDefaults(t *testing.T) {
	s := &CreateStyleNode{Name: "styles"}
	if s.Kind() != NodeCreateStyle || s.NodeID() != "style-styles" || s.NodeName() != "styles" {
		t.Fatalf("style node = %+v", s)
	}
	c := &CreateScriptNode{Name: "script", Behavior: "toggle nav"}
	if c.Kind() != NodeCreateScript || c.NodeID() != "script-script" {
		t.Fatalf("script node = %+v", c)
	}
}

func TestLogicalPlanConstructionAndAccessors(t *testing.T) {
	page := &CreatePageNode{Name: "About"}
	section := &CreateSectionNode{Name: "Hero"}
	rel, err := NewRelation(page.NodeID(), section.NodeID(), RelationComposes)
	if err != nil {
		t.Fatalf("NewRelation: %v", err)
	}
	plan, err := NewLogicalPlan([]IRNode{page, section}, []Relation{rel})
	if err != nil {
		t.Fatalf("NewLogicalPlan: %v", err)
	}
	if plan.Len() != 2 {
		t.Fatalf("Len = %d, want 2", plan.Len())
	}
	if got := plan.KindCount(NodeCreatePage); got != 1 {
		t.Fatalf("KindCount(create_page) = %d, want 1", got)
	}
	if got := plan.KindCount(NodeCreateSection); got != 1 {
		t.Fatalf("KindCount(create_section) = %d, want 1", got)
	}
	if got := plan.Relations(); len(got) != 1 || got[0] != rel {
		t.Fatalf("Relations = %+v, want %+v", got, rel)
	}
	found, ok := plan.Node(page.NodeID())
	if !ok || found.NodeName() != "About" {
		t.Fatalf("Node(%q) = %+v, %v", page.NodeID(), found, ok)
	}
	out := plan.OutgoingRelations(page.NodeID())
	if len(out) != 1 || out[0].To != section.NodeID() {
		t.Fatalf("OutgoingRelations = %+v", out)
	}
}

func TestLogicalPlanRejectsEmpty(t *testing.T) {
	if _, err := NewLogicalPlan(nil, nil); err == nil {
		t.Fatal("expected error for empty plan")
	}
}

func TestLogicalPlanRejectsDuplicateNodeIDs(t *testing.T) {
	a := &CreatePageNode{ID: "dup", Name: "A"}
	b := &CreatePageNode{ID: "dup", Name: "B"}
	if _, err := NewLogicalPlan([]IRNode{a, b}, nil); err == nil {
		t.Fatal("expected error for duplicate node ids")
	}
}

func TestLogicalPlanRejectsRelationToUnknownNode(t *testing.T) {
	page := &CreatePageNode{Name: "About"}
	rel, _ := NewRelation("page-about", "ghost", RelationDependsOn)
	if _, err := NewLogicalPlan([]IRNode{page}, []Relation{rel}); err == nil {
		t.Fatal("expected error for relation to unknown node")
	}
}

func TestLogicalPlanIsImmutableCopy(t *testing.T) {
	page := &CreatePageNode{Name: "About"}
	plan, err := NewLogicalPlan([]IRNode{page}, nil)
	if err != nil {
		t.Fatalf("NewLogicalPlan: %v", err)
	}
	// Mutating the source slice after construction must not affect the plan.
	page.Name = "Contact"
	got, _ := plan.Node("page-about")
	if got.NodeName() != "About" {
		t.Fatalf("plan node name mutated to %q", got.NodeName())
	}
}

func TestRelationValidation(t *testing.T) {
	if _, err := NewRelation("", "b", RelationDependsOn); err == nil {
		t.Fatal("expected error for empty from")
	}
	if _, err := NewRelation("a", "b", RelationKind("bogus")); err == nil {
		t.Fatal("expected error for bogus kind")
	}
}

func TestNodeImmutableAcrossReuse(t *testing.T) {
	// The verification contract: the SAME node value reused across adapter
	// runs stays byte-identical.
	original := &CreatePageNode{Name: "About"}
	before := *original
	_ = original.SectionNames()
	_ = original.NodeID()
	if !reflect.DeepEqual(original, &before) {
		t.Fatal("node value changed after accessor reads")
	}
}
