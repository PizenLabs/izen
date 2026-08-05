package adapter

import (
	"reflect"
	"testing"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// Test1_LogicalIRReusability is the primary verification contract of the
// IR-driven intent compiler: the EXACT same CreatePageNode{Name: "About"} —
// not a clone, not a copy — renders through different framework adapters into
// different physical layouts, and the Logical IR value stays 100% identical
// across both runs.
func Test1_LogicalIRReusability(t *testing.T) {
	page := &ir.CreatePageNode{Name: "About"}
	pristine := &ir.CreatePageNode{Name: "About"}

	nextJS := NewNextJSAdapter()
	nextArts, err := nextJS.RenderNode(page)
	if err != nil {
		t.Fatalf("NextJSAdapter.RenderNode: %v", err)
	}
	if len(nextArts) != 1 {
		t.Fatalf("NextJSAdapter produced %d artifacts, want 1", len(nextArts))
	}
	if nextArts[0].Path != "app/about/page.tsx" {
		t.Errorf("NextJSAdapter path = %q, want %q", nextArts[0].Path, "app/about/page.tsx")
	}

	astro := NewAstroAdapter()
	astroArts, err := astro.RenderNode(page)
	if err != nil {
		t.Fatalf("AstroAdapter.RenderNode: %v", err)
	}
	if len(astroArts) != 1 {
		t.Fatalf("AstroAdapter produced %d artifacts, want 1", len(astroArts))
	}
	if astroArts[0].Path != "src/pages/about.astro" {
		t.Errorf("AstroAdapter path = %q, want %q", astroArts[0].Path, "src/pages/about.astro")
	}

	// Logical IR is 100% identical between both runs: neither adapter mutated
	// the shared node value.
	if !reflect.DeepEqual(page, pristine) {
		t.Fatalf("Logical IR mutated across adapter runs:\n got  %+v\n want %+v", page, pristine)
	}
	if page.Name != "About" {
		t.Fatalf("node Name mutated to %q", page.Name)
	}

	// The two renderings must differ physically.
	if nextArts[0].Path == astroArts[0].Path {
		t.Errorf("different adapters must render different layouts, both %q", nextArts[0].Path)
	}
}

func TestNextJSAdapterRendersAllNodeKinds(t *testing.T) {
	a := NewNextJSAdapter()
	cases := []struct {
		node ir.IRNode
		want string
	}{
		{&ir.CreatePageNode{Name: "About"}, "app/about/page.tsx"},
		{&ir.CreatePageNode{Name: "User Profile"}, "app/user-profile/page.tsx"},
		{&ir.CreateSectionNode{Name: "Hero"}, "app/components/hero.tsx"},
		{&ir.CreateComponentNode{Name: "Navbar"}, "app/components/navbar.tsx"},
		{&ir.CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"}, "app/api/users/route.ts"},
		{&ir.CreateDatabaseMigrationNode{Name: "create_users_table"}, "migrations/create-users-table.sql"},
		{&ir.CreateStyleNode{Name: "styles"}, "app/styles.css"},
		{&ir.CreateScriptNode{Name: "script"}, "app/lib/script.ts"},
	}
	for _, tc := range cases {
		arts, err := a.RenderNode(tc.node)
		if err != nil {
			t.Fatalf("%T: %v", tc.node, err)
		}
		if len(arts) != 1 || arts[0].Path != tc.want {
			t.Fatalf("%T path = %+v, want %q", tc.node, arts, tc.want)
		}
	}
}

func TestAstroAdapterRendersAllNodeKinds(t *testing.T) {
	a := NewAstroAdapter()
	cases := []struct {
		node ir.IRNode
		want string
	}{
		{&ir.CreatePageNode{Name: "About"}, "src/pages/about.astro"},
		{&ir.CreateSectionNode{Name: "Hero"}, "src/components/hero.astro"},
		{&ir.CreateComponentNode{Name: "Navbar"}, "src/components/navbar.astro"},
		{&ir.CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"}, "src/pages/api/users.ts"},
		{&ir.CreateDatabaseMigrationNode{Name: "create_users_table"}, "db/create-users-table.sql"},
		{&ir.CreateStyleNode{Name: "styles"}, "src/styles/styles.css"},
		{&ir.CreateScriptNode{Name: "script"}, "src/scripts/script.ts"},
	}
	for _, tc := range cases {
		arts, err := a.RenderNode(tc.node)
		if err != nil {
			t.Fatalf("%T: %v", tc.node, err)
		}
		if len(arts) != 1 || arts[0].Path != tc.want {
			t.Fatalf("%T path = %+v, want %q", tc.node, arts, tc.want)
		}
	}
}

func TestReactViteAdapterRendersAllNodeKinds(t *testing.T) {
	a := NewReactViteAdapter()
	cases := []struct {
		node ir.IRNode
		want string
	}{
		{&ir.CreatePageNode{Name: "About"}, "src/pages/About.tsx"},
		{&ir.CreateSectionNode{Name: "Hero"}, "src/components/Hero.tsx"},
		{&ir.CreateComponentNode{Name: "Navbar"}, "src/components/Navbar.tsx"},
		{&ir.CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"}, "src/handlers/listusers.ts"},
		{&ir.CreateDatabaseMigrationNode{Name: "create_users_table"}, "db/migrations/create-users-table.sql"},
		{&ir.CreateStyleNode{Name: "styles"}, "src/styles.css"},
		{&ir.CreateScriptNode{Name: "script"}, "src/script.ts"},
	}
	for _, tc := range cases {
		arts, err := a.RenderNode(tc.node)
		if err != nil {
			t.Fatalf("%T: %v", tc.node, err)
		}
		if len(arts) != 1 || arts[0].Path != tc.want {
			t.Fatalf("%T path = %+v, want %q", tc.node, arts, tc.want)
		}
	}
}

func TestGoGinAdapterRendersAllNodeKinds(t *testing.T) {
	a := NewGoGinAdapter()
	cases := []struct {
		node ir.IRNode
		want string
	}{
		{&ir.CreatePageNode{Name: "About"}, "templates/about.html"},
		{&ir.CreateSectionNode{Name: "Hero"}, "templates/partials/hero.html"},
		{&ir.CreateComponentNode{Name: "Navbar"}, "templates/partials/navbar.html"},
		{&ir.CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"}, "internal/handlers/list_users.go"},
		{&ir.CreateDatabaseMigrationNode{Name: "create_users_table"}, "migrations/create_users_table.sql"},
		{&ir.CreateStyleNode{Name: "styles"}, "static/styles.css"},
		{&ir.CreateScriptNode{Name: "script"}, "static/script.js"},
	}
	for _, tc := range cases {
		arts, err := a.RenderNode(tc.node)
		if err != nil {
			t.Fatalf("%T: %v", tc.node, err)
		}
		if len(arts) != 1 || arts[0].Path != tc.want {
			t.Fatalf("%T path = %+v, want %q", tc.node, arts, tc.want)
		}
	}
}

// TestStaticWebAdapterRendersAllNodeKinds is the verification contract for the
// static HTML/CSS/JS renderer: the greenfield page lowers to index.html, the
// style node to styles.css and the script node to script.js.
func TestStaticWebAdapterRendersAllNodeKinds(t *testing.T) {
	a := NewStaticWebAdapter()
	cases := []struct {
		node ir.IRNode
		want string
	}{
		{&ir.CreatePageNode{Name: "index"}, "index.html"},
		{&ir.CreatePageNode{Name: "about"}, "about.html"},
		{&ir.CreateSectionNode{Name: "Hero"}, "sections/hero.html"},
		{&ir.CreateComponentNode{Name: "Navbar"}, "partials/navbar.html"},
		{&ir.CreateStyleNode{Name: "styles"}, "styles.css"},
		{&ir.CreateScriptNode{Name: "script"}, "script.js"},
		{&ir.CreateDatabaseMigrationNode{Name: "create_users_table"}, "db/create-users-table.sql"},
	}
	for _, tc := range cases {
		arts, err := a.RenderNode(tc.node)
		if err != nil {
			t.Fatalf("%T: %v", tc.node, err)
		}
		if len(arts) != 1 || arts[0].Path != tc.want {
			t.Fatalf("%T path = %+v, want %q", tc.node, arts, tc.want)
		}
		if arts[0].Content == "" {
			t.Fatalf("%T rendered empty content", tc.node)
		}
	}

	// A dynamic endpoint cannot be rendered on a static site.
	if _, err := a.RenderNode(&ir.CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"}); err == nil {
		t.Fatal("static-web must reject API endpoints")
	}
}

func TestAdaptersRejectUnknownNodes(t *testing.T) {
	unknown := fakeNode{}
	adapters := []FrameworkAdapter{
		NewNextJSAdapter(), NewAstroAdapter(), NewReactViteAdapter(), NewGoGinAdapter(),
	}
	for _, a := range adapters {
		if _, err := a.RenderNode(unknown); err == nil {
			t.Fatalf("%v accepted an unknown node", a.Framework())
		}
	}
}

func TestRenderedContentIsNonEmpty(t *testing.T) {
	a := NewNextJSAdapter()
	arts, err := a.RenderNode(&ir.CreatePageNode{Name: "About"})
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}
	if len(arts) != 1 || arts[0].Content == "" {
		t.Fatalf("rendered page artifact has no content: %+v", arts)
	}
}

// fakeNode is an unknown IRNode used to prove adapters reject unsupported
// kinds.
type fakeNode struct{}

func (fakeNode) Kind() ir.NodeKind { return ir.NodeKind("bogus") }
func (fakeNode) NodeID() string    { return "bogus" }
func (fakeNode) NodeName() string  { return "bogus" }
func (fakeNode) NodeDesc() string  { return "bogus" }
