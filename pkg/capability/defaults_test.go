package capability

import (
	"context"
	"strings"
	"testing"
)

const portfolioHTML = `<!DOCTYPE html>
<html><head><title>My Portfolio</title></head><body>
<header><nav><a href="#about">About</a><a href="#projects">Projects</a></nav></header>
<main>
<section id="about"><h1>About me</h1></section>
<section id="projects"><article><h2>Project one</h2></article></section>
<section id="contact"><h2>Contact</h2></section>
</main>
<footer>&copy; 2026</footer>
</body></html>`

const todoAppHTML = `<!DOCTYPE html>
<html><head><title>Todo App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div><button onclick="addTask()">Add</button></div>
<div id="taskList"></div>
<script>
let todos = [];
function addTask() { todos.push(document.getElementById("newTodo").value); }
</script>
</body></html>`

func TestRegisterDefaults(t *testing.T) {
	r := NewRegistry()
	if err := RegisterDefaults(r); err != nil {
		t.Fatal(err)
	}
	for _, id := range []CapabilityID{CapPortfolioWebsite, CapSemanticHTML, CapTypeScript, CapGoBackend, CapGenericCode} {
		if !r.Has(id) {
			t.Errorf("missing default capability %s", id)
		}
	}
	if err := RegisterDefaults(r); err != nil {
		t.Errorf("RegisterDefaults must be idempotent, got %v", err)
	}
	if err := RegisterDefaults(nil); !errorsIsNilRegistry(err) {
		t.Errorf("RegisterDefaults(nil) error = %v, want ErrNilRegistry", err)
	}
}

func errorsIsNilRegistry(err error) bool {
	return err != nil && strings.Contains(err.Error(), "nil registry")
}

func TestDefaultCapabilitiesContract(t *testing.T) {
	caps := DefaultCapabilities()
	if len(caps) == 0 {
		t.Fatal("no default capabilities")
	}
	for _, c := range caps {
		if c.ID() == "" {
			t.Error("capability with empty id")
		}
		if c.Description() == "" {
			t.Errorf("%s: empty description", c.ID())
		}
		if c.PromptRepresentation("small") == "" {
			t.Errorf("%s: empty small prompt", c.ID())
		}
		if c.PromptRepresentation("full") == "" {
			t.Errorf("%s: empty full prompt", c.ID())
		}
		if c.PromptRepresentation("full") == c.PromptRepresentation("small") {
			t.Errorf("%s: full and small prompts must differ", c.ID())
		}
	}
}

func TestPortfolioCapabilityRejectsTodoApp(t *testing.T) {
	c := NewPortfolioWebsite()
	res := c.Validate(context.Background(), []byte(todoAppHTML))
	if res.Passed {
		t.Fatalf("to-do app must fail portfolio validation: %+v", res)
	}
}

func TestPortfolioCapabilityAcceptsPortfolio(t *testing.T) {
	c := NewPortfolioWebsite()
	res := c.Validate(context.Background(), []byte(portfolioHTML))
	if !res.Passed {
		t.Fatalf("portfolio HTML must pass: %+v", res)
	}
}

func TestPortfolioCapabilityIgnoresNonWebArtifacts(t *testing.T) {
	c := NewPortfolioWebsite()
	res := c.Validate(context.Background(), []byte("# Notes\nplain markdown with no web markup"))
	if !res.Passed {
		t.Errorf("non-web artifact must pass: %+v", res)
	}
}

func TestPortfolioPromptForbidsFallbackTemplates(t *testing.T) {
	c := NewPortfolioWebsite()
	full := c.PromptRepresentation("full")
	for _, marker := range []string{"portfolio", "forbidden", "to-do", "violation"} {
		if !strings.Contains(strings.ToLower(full), marker) {
			t.Errorf("full portfolio prompt missing constraint marker %q", marker)
		}
	}
}

func TestSemanticHTMLRejectsDivSoup(t *testing.T) {
	c := NewSemanticHTML()
	divSoup := `<!DOCTYPE html><html><head><title>x</title></head><body>
<div class="container"><div class="row"><div class="col">
<div class="box"><div class="inner"><div class="card"></div></div></div>
</div></div></div></body></html>`
	res := c.Validate(context.Background(), []byte(divSoup))
	if res.Passed {
		t.Fatalf("div-soup HTML must fail: %+v", res)
	}
}

func TestSemanticHTMLAcceptsLandmarks(t *testing.T) {
	c := NewSemanticHTML()
	res := c.Validate(context.Background(), []byte(portfolioHTML))
	if !res.Passed {
		t.Fatalf("semantic HTML must pass: %+v", res)
	}
}

func TestTypeScriptRejectsPlainJS(t *testing.T) {
	c := NewTypeScript()
	plain := `export function add(a, b) { return a + b; }
const todos = [];
document.querySelector("#app").innerHTML = "x";`
	res := c.Validate(context.Background(), []byte(plain))
	if res.Passed {
		t.Fatalf("plain JS module must fail TypeScript validation: %+v", res)
	}
}

func TestTypeScriptAcceptsTypedModule(t *testing.T) {
	c := NewTypeScript()
	typed := `interface Todo { id: number; title: string }
export function add(a: number, b: number): number { return a + b; }`
	res := c.Validate(context.Background(), []byte(typed))
	if !res.Passed {
		t.Fatalf("typed TS module must pass: %+v", res)
	}
}

func TestGoBackendRejectsMissingPackage(t *testing.T) {
	c := NewGoBackend()
	res := c.Validate(context.Background(), []byte("func main() { println(\"hi\") }"))
	if res.Passed {
		t.Fatalf("Go source without package clause must fail: %+v", res)
	}
}

func TestGoBackendAcceptsPackagedSource(t *testing.T) {
	c := NewGoBackend()
	res := c.Validate(context.Background(), []byte("package main\n\nfunc main() {}\n"))
	if !res.Passed {
		t.Fatalf("packaged Go source must pass: %+v", res)
	}
}

func TestGenericCodeAlwaysPasses(t *testing.T) {
	c := NewGenericCode()
	res := c.Validate(context.Background(), []byte("anything"))
	if !res.Passed {
		t.Fatalf("generic code must always pass: %+v", res)
	}
}

func TestDefaultCapabilitiesSelfScoping(t *testing.T) {
	// Capabilities must not fail artifacts outside their domain.
	nonHTML := []byte("# Just a readme")
	if res := NewPortfolioWebsite().Validate(context.Background(), nonHTML); !res.Passed {
		t.Errorf("portfolio rejected non-web artifact: %+v", res)
	}
	if res := NewSemanticHTML().Validate(context.Background(), nonHTML); !res.Passed {
		t.Errorf("semantic html rejected non-html artifact: %+v", res)
	}
	if res := NewTypeScript().Validate(context.Background(), nonHTML); !res.Passed {
		t.Errorf("typescript rejected non-script artifact: %+v", res)
	}
	if res := NewGoBackend().Validate(context.Background(), nonHTML); !res.Passed {
		t.Errorf("go rejected non-go artifact: %+v", res)
	}
}
