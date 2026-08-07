package compiler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/pkg/ir"
)

// --- fixtures -------------------------------------------------------------

// todoWorkspace materialises a recognisable To-Do App workspace: an
// index.html carrying the classic task-list structure and a todo.js with the
// canonical todos state variable.
func todoWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), `<!DOCTYPE html>
<html><head><title>Todo App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div><button onclick="addTask()">Add</button></div>
<div id="taskList"></div>
<script src="todo.js"></script>
</body></html>`)
	writeFile(t, filepath.Join(root, "todo.js"), `let todos = [];
function addTask() { todos.push(document.getElementById("newTodo").value); render(); }
function render() { document.getElementById("taskList").textContent = todos.join(","); }`)
	return root
}

// portfolioWorkspace materialises a recognisable Portfolio workspace.
func portfolioWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), `<!DOCTYPE html>
<html><head><title>My Portfolio</title></head><body>
<main><section id="about"><h1>About me</h1></section></main>
</body></html>`)
	writeFile(t, filepath.Join(root, "styles.css"), "body { font-family: sans-serif; }")
	return root
}

// emptyWorkspace returns a fresh, empty workspace root.
func emptyWorkspace(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// --- semantic extractor stub ----------------------------------------------

// stubExtractor is a race-safe stand-in for a zero-shot semantic model. It
// returns a scripted JSON document (or error) and records the exact prompts
// it was handed so tests can assert the schema contract.
type stubExtractor struct {
	mu         sync.Mutex
	out        string
	err        error
	calls      int
	lastSystem string
	lastPrompt string
}

func (s *stubExtractor) Extract(_ context.Context, system, prompt string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastSystem = system
	s.lastPrompt = prompt
	if s.err != nil {
		return "", s.err
	}
	return s.out, nil
}

// redesignPortfolioJSON is the canonical semantic extraction for a
// "redesign Alex Josie's site as a software engineer portfolio, not a todo
// app" intent. It is returned by the stub for every language variant, which
// is exactly what a real zero-shot model is expected to do: bind any source
// language onto the same English canonical schema.
const redesignPortfolioJSON = `{
  "category": "redesign",
  "target_type": "portfolio",
  "entities": {"author": "Alex Josie", "role": "software engineer"},
  "technologies": [],
  "negated_targets": ["todo_app"]
}`

// compileStub runs the full compiler over raw with a stub-backed extractor
// and fails the test on error.
func compileStub(t *testing.T, root string, stub *stubExtractor, raw string) ir.IntentIR {
	t.Helper()
	c := NewIntentCompiler(root, stub)
	got, err := c.Compile(t.Context(), raw)
	if err != nil {
		t.Fatalf("Compile(%q): %v", raw, err)
	}
	return got
}

// resolveStub runs just the resolver stage over raw with a stub-backed
// extractor and fails the test on error.
func resolveStub(t *testing.T, stub *stubExtractor, raw string) Resolution {
	t.Helper()
	res, err := NewEntityResolver(stub).Process(t.Context(), raw)
	if err != nil {
		t.Fatalf("Process(%q): %v", raw, err)
	}
	return res
}

// --- acceptance: English primary + multi-language -------------------------

// TestCompileEnglishPrimary is the primary English acceptance scenario. A
// redesign over an existing To-Do App workspace must compile to a redesign
// portfolio with the author bound and a flagged decision ambiguity.
func TestCompileEnglishPrimary(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	got := compileStub(t, todoWorkspace(t), stub,
		"Redesign website for Alex Josie as a software engineer portfolio, not a todo app")

	if got.Category != ir.CategoryRedesign {
		t.Errorf("Category = %s, want %s", got.Category, ir.CategoryRedesign)
	}
	if got.TargetType != "portfolio" {
		t.Errorf("TargetType = %q, want %q", got.TargetType, "portfolio")
	}
	if got.Entities["author"] != "Alex Josie" {
		t.Errorf("Entities[author] = %q, want %q", got.Entities["author"], "Alex Josie")
	}
	if !got.DecisionAmbiguity {
		t.Error("DecisionAmbiguity = false, want true (todo app workspace conflict)")
	}
	if got.PreserveWorkspace {
		t.Error("PreserveWorkspace = true, want false for a redesign")
	}
	if len(got.ClarificationQuestions) == 0 {
		t.Error("expected a clarification question for the workspace conflict")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestCompileChinese is the global language scenario: a Chinese prompt binds
// to the same English canonical IntentIR.
func TestCompileChinese(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	got := compileStub(t, todoWorkspace(t), stub,
		"为 Alex Josie 重新设计软件工程师个人主页网站，不要 todo app")

	if got.Category != ir.CategoryRedesign {
		t.Errorf("Category = %s, want %s", got.Category, ir.CategoryRedesign)
	}
	if got.TargetType != "portfolio" {
		t.Errorf("TargetType = %q, want %q", got.TargetType, "portfolio")
	}
	if got.Entities["author"] != "Alex Josie" {
		t.Errorf("Entities[author] = %q, want %q", got.Entities["author"], "Alex Josie")
	}
	if !got.DecisionAmbiguity {
		t.Error("DecisionAmbiguity = false, want true (negated todo_app over todo workspace)")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestCompileMixed is the mixed-language scenario: a Vietnamese + English
// prompt binds to the same English canonical IntentIR.
func TestCompileMixed(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	got := compileStub(t, todoWorkspace(t), stub,
		"lam lai website ho Alex Josie, redesign not todo app")

	if got.Category != ir.CategoryRedesign {
		t.Errorf("Category = %s, want %s", got.Category, ir.CategoryRedesign)
	}
	if got.TargetType != "portfolio" {
		t.Errorf("TargetType = %q, want %q", got.TargetType, "portfolio")
	}
	if got.Entities["author"] != "Alex Josie" {
		t.Errorf("Entities[author] = %q, want %q", got.Entities["author"], "Alex Josie")
	}
	if !got.DecisionAmbiguity {
		t.Error("DecisionAmbiguity = false, want true (todo app workspace conflict)")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestCompileLanguageAgnosticEquivalence proves the compiler is
// language-agnostic end to end: the same semantic extraction produces a
// byte-for-byte identical IntentIR whether the prompt is English, Chinese or
// mixed, on the same workspace.
func TestCompileLanguageAgnosticEquivalence(t *testing.T) {
	inputs := []string{
		"Redesign website for Alex Josie as a software engineer portfolio, not a todo app",
		"为 Alex Josie 重新设计软件工程师个人主页网站，不要 todo app",
		"lam lai website ho Alex Josie, redesign not todo app",
	}
	root := todoWorkspace(t)
	base := compileStub(t, root, &stubExtractor{out: redesignPortfolioJSON}, inputs[0])
	for _, in := range inputs[1:] {
		got := compileStub(t, root, &stubExtractor{out: redesignPortfolioJSON}, in)
		if !reflect.DeepEqual(got, base) {
			t.Errorf("language variant produced a different IntentIR\nin : %q\ngot: %#v\nwant: %#v", in, got, base)
		}
	}
}

// TestResolverSemanticSchemaPrompt asserts the resolver drives a zero-shot
// JSON schema prompt whose canonical labels are English, and passes the
// normalised input through.
func TestResolverSemanticSchemaPrompt(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	res := resolveStub(t, stub, "Redesign website for Alex Josie as a software engineer portfolio, not a todo app")

	if stub.calls != 1 {
		t.Errorf("extractor calls = %d, want 1", stub.calls)
	}
	for _, want := range []string{"category", "target_type", "negated_targets", "portfolio", "todo_app", "redesign", "fix_bug"} {
		if !strings.Contains(stub.lastSystem, want) {
			t.Errorf("system prompt missing canonical token %q", want)
		}
	}
	if !strings.Contains(stub.lastPrompt, "Redesign website for Alex Josie") {
		t.Errorf("prompt = %q, want the normalised input", stub.lastPrompt)
	}
	if res.Category != ir.CategoryRedesign || res.TargetType != "portfolio" {
		t.Errorf("resolution = %+v", res)
	}
	if !res.Negated["todo_app"] {
		t.Errorf("Negated = %v, want todo_app", res.Negated)
	}
}

// TestCompilePassesNormalisedInput verifies the compiler normalises (NFC,
// whitespace) before handing the prompt to the extractor.
func TestCompilePassesNormalisedInput(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	c := NewIntentCompiler(emptyWorkspace(t), stub)
	if _, err := c.Compile(t.Context(), "   Redesign   website   for   Alex   Josie   "); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if stub.lastPrompt != "Request: Redesign website for Alex Josie" {
		t.Errorf("extractor prompt = %q, want collapsed single-space input", stub.lastPrompt)
	}
}

// TestResolverAcceptsFencedJSON verifies fence-wrapped model output is
// unwrapped before decoding.
func TestResolverAcceptsFencedJSON(t *testing.T) {
	stub := &stubExtractor{out: "```json\n" + redesignPortfolioJSON + "\n```"}
	res := resolveStub(t, stub, "Redesign website for Alex Josie")
	if res.Category != ir.CategoryRedesign || res.TargetType != "portfolio" {
		t.Errorf("resolution = %+v", res)
	}
	if res.Entities["author"] != "Alex Josie" {
		t.Errorf("author = %q", res.Entities["author"])
	}
}

// --- Normalizer: language-agnostic ----------------------------------------

func TestNormalizerUnicodeNFC(t *testing.T) {
	n := NewNormalizer()
	cases := map[string]string{
		// a + U+0302 (combining circumflex) must compose to â.
		"lam la\u0302i": "lam lâi",
		// e + U+0301 (combining acute) must compose to é.
		"cafe\u0301": "café",
	}
	for in, want := range cases {
		if got := n.Process(in); got != want {
			t.Errorf("Process(%q) = %q, want NFC %q", in, got, want)
		}
	}
}

func TestNormalizerStripsControlAndCollapses(t *testing.T) {
	n := NewNormalizer()
	cases := map[string]string{
		"  Build   a   website  ":   "Build a website",
		"build\x00a\x01website":     "buildawebsite", // control chars stripped, nothing substituted
		"build \x00 a \x01 website": "build a website",
		"build\x1b[31mwebsite":      "build[31mwebsite", // ESC is control, printable text kept
		"tab\tseparated\twords":     "tab separated words",
		"line\nbreak\nkept":         "line break kept",
	}
	for in, want := range cases {
		if got := n.Process(in); got != want {
			t.Errorf("Process(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizerPreservesUTF8(t *testing.T) {
	n := NewNormalizer()
	for _, in := range []string{
		"为 Alex Josie 重新设计网站",
		"مرحبا بالعالم",
		"создать сайт",
		"build a 🚀 site",
	} {
		if got := n.Process(in); got != in {
			t.Errorf("Process(%q) = %q, UTF-8 must be preserved verbatim", in, got)
		}
	}
}

func TestNormalizerDoesNotTranslate(t *testing.T) {
	// Language-agnostic means the Normalizer NEVER rewrites raw words: no
	// Vietnamese/English dictionaries exist in Go source.
	n := NewNormalizer()
	for _, in := range []string{
		"lam lai website ho Alex Josie",
		"Redesign website",
		"lam laij",
		//nolint:misspell // deliberate typo to prove no word rewriting
		"webiste",
	} {
		if got := n.Process(in); got != in {
			t.Errorf("Process(%q) = %q, want verbatim (no word rewriting)", in, got)
		}
	}
}

func TestNormalizerIdempotentAndEmpty(t *testing.T) {
	n := NewNormalizer()
	once := n.Process("   build   a   website   ")
	if got := n.Process(once); got != once {
		t.Errorf("Process not idempotent: %q -> %q", once, got)
	}
	if got := n.Process(""); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
	if got := n.Process("   "); got != "" {
		t.Errorf("whitespace input = %q, want empty", got)
	}
}

// --- Resolver: schema validation -------------------------------------------

func TestResolverRejectsUnknownCategory(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"write","target_type":"website","entities":{},"negated_targets":[]}`}
	if _, err := NewEntityResolver(stub).Process(t.Context(), "build a website"); !errors.Is(err, ErrInvalidExtraction) {
		t.Fatalf("error = %v, want ErrInvalidExtraction", err)
	}
}

func TestResolverRejectsUnknownTargetType(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"create","target_type":"microsite","entities":{},"negated_targets":[]}`}
	if _, err := NewEntityResolver(stub).Process(t.Context(), "build a website"); !errors.Is(err, ErrInvalidExtraction) {
		t.Fatalf("error = %v, want ErrInvalidExtraction", err)
	}
}

func TestResolverRejectsMalformedJSON(t *testing.T) {
	stub := &stubExtractor{out: "this is not json at all"}
	if _, err := NewEntityResolver(stub).Process(t.Context(), "build a website"); !errors.Is(err, ErrInvalidExtraction) {
		t.Fatalf("error = %v, want ErrInvalidExtraction", err)
	}
}

func TestResolverDefaultTechnologies(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"create","target_type":"portfolio","entities":{"author":"Alex Josie"},"technologies":[],"negated_targets":[]}`}
	res := resolveStub(t, stub, "build a portfolio for Alex Josie")
	if len(res.Technologies) != 3 || res.Technologies[0] != "html" {
		t.Errorf("Technologies = %v, want default [html css js]", res.Technologies)
	}
}

func TestResolverCanonicalTechnologies(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"create","target_type":"portfolio","entities":{},"technologies":["React","react","flutter","Typescript"],"negated_targets":[]}`}
	res := resolveStub(t, stub, "build a portfolio")
	if !reflect.DeepEqual(res.Technologies, []string{"react", "typescript"}) {
		t.Errorf("Technologies = %v, want [react typescript] (lowercased, de-duped, unknown dropped)", res.Technologies)
	}
}

func TestResolverEmptyInputShortCircuit(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	res, err := NewEntityResolver(stub).Process(t.Context(), "   ")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Category != ir.CategoryCreate || res.TargetType != "" {
		t.Errorf("resolution = %+v, want default create", res)
	}
	if stub.calls != 0 {
		t.Error("extractor must not be called for empty input")
	}
}

// --- error propagation -----------------------------------------------------

func TestCompileRequiresExtractor(t *testing.T) {
	c := NewIntentCompiler(t.TempDir(), nil)
	if _, err := c.Compile(t.Context(), "build a website"); !errors.Is(err, ErrNoExtractor) {
		t.Fatalf("error = %v, want ErrNoExtractor", err)
	}
}

func TestCompilePropagatesExtractionError(t *testing.T) {
	stub := &stubExtractor{err: errors.New("model timeout")}
	c := NewIntentCompiler(t.TempDir(), stub)
	if _, err := c.Compile(t.Context(), "build a website"); err == nil || !strings.Contains(err.Error(), "model timeout") {
		t.Fatalf("error = %v, want propagated model timeout", err)
	}
}

// --- compiler integration ---------------------------------------------------

func TestCompileEmptyPrompt(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	c := NewIntentCompiler(emptyWorkspace(t), stub)
	got, err := c.Compile(t.Context(), "   ")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got.Category != ir.CategoryCreate {
		t.Errorf("Category = %s, want default create", got.Category)
	}
	if got.TargetType != "" {
		t.Errorf("TargetType = %q, want empty", got.TargetType)
	}
	if stub.calls != 0 {
		t.Error("extractor must not be called for an empty prompt")
	}
	if err := got.Validate(); err == nil {
		t.Error("Validate = nil for an empty target, want error")
	}
}

func TestCompileTodoAppRedesignMatchesWorkspace(t *testing.T) {
	// Redesigning the todo app over a todo app workspace is unambiguous.
	stub := &stubExtractor{out: `{"category":"redesign","target_type":"todo_app","entities":{},"technologies":[],"negated_targets":[]}`}
	got := compileStub(t, todoWorkspace(t), stub, "Redesign the todo app")
	if got.TargetType != "todo_app" {
		t.Errorf("TargetType = %q, want todo_app", got.TargetType)
	}
	if got.DecisionAmbiguity {
		t.Error("DecisionAmbiguity = true when target matches workspace")
	}
}

func TestCompileCreatePortfolioOverTodoWorkspace(t *testing.T) {
	// A create over a conflicting workspace is still ambiguous.
	stub := &stubExtractor{out: `{"category":"create","target_type":"portfolio","entities":{},"technologies":[],"negated_targets":[]}`}
	got := compileStub(t, todoWorkspace(t), stub, "create a portfolio website")
	if got.Category != ir.CategoryCreate {
		t.Errorf("Category = %s, want create", got.Category)
	}
	if got.TargetType != "portfolio" {
		t.Errorf("TargetType = %q, want portfolio", got.TargetType)
	}
	if !got.DecisionAmbiguity {
		t.Error("DecisionAmbiguity = false for conflicting create")
	}
	if !got.PreserveWorkspace {
		t.Error("PreserveWorkspace = false for a create")
	}
}

func TestCompileFixBug(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"fix_bug","target_type":"todo_app","entities":{},"technologies":[],"negated_targets":[]}`}
	got := compileStub(t, todoWorkspace(t), stub, "fix the broken add button on the todo app")
	if got.Category != ir.CategoryFixBug {
		t.Errorf("Category = %s, want fix_bug", got.Category)
	}
	if got.TargetType != "todo_app" {
		t.Errorf("TargetType = %q, want todo_app", got.TargetType)
	}
	if !got.PreserveWorkspace {
		t.Error("PreserveWorkspace = false for a bug fix")
	}
}

func TestCompileRefactorOverEmptyWorkspace(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"refactor","target_type":"rest_api","entities":{},"technologies":[],"negated_targets":[]}`}
	got := compileStub(t, emptyWorkspace(t), stub, "refactor the backend api handlers")
	if got.Category != ir.CategoryRefactor {
		t.Errorf("Category = %s, want refactor", got.Category)
	}
	if got.TargetType != "rest_api" {
		t.Errorf("TargetType = %q, want rest_api", got.TargetType)
	}
	if got.DecisionAmbiguity {
		t.Error("DecisionAmbiguity = true on empty workspace")
	}
}

func TestCompileCopiesMapsAndSlices(t *testing.T) {
	stub := &stubExtractor{out: `{"category":"redesign","target_type":"portfolio","entities":{"author":"Alex Josie"},"technologies":["react"],"negated_targets":["todo_app"]}`}
	c := NewIntentCompiler(todoWorkspace(t), stub)
	first, err := c.Compile(t.Context(), "Redesign the portfolio")
	if err != nil {
		t.Fatal(err)
	}
	first.Entities["tampered"] = "yes"
	first.Technologies[0] = "tampered"

	second, err := c.Compile(t.Context(), "Redesign the portfolio")
	if err != nil {
		t.Fatal(err)
	}
	if second.Entities["tampered"] != "" {
		t.Error("compiler aliased entity map between compiles")
	}
	if second.Technologies[0] == "tampered" {
		t.Error("compiler aliased technology slice between compiles")
	}
}

// --- ConflictDetector -------------------------------------------------------

func TestDetectWorkspaceTodoApp(t *testing.T) {
	ws := NewConflictDetector().Detect(todoWorkspace(t))
	if ws.Empty {
		t.Error("todo workspace reported empty")
	}
	if !ws.AppTypes["todo_app"] {
		t.Errorf("AppTypes = %v, want todo_app", ws.AppTypes)
	}
	if !ws.Archetypes["vanilla_web"] {
		t.Errorf("Archetypes = %v, want vanilla_web", ws.Archetypes)
	}
	if len(ws.Markers) == 0 {
		t.Error("expected markers")
	}
}

func TestDetectWorkspacePortfolio(t *testing.T) {
	ws := NewConflictDetector().Detect(portfolioWorkspace(t))
	if ws.Empty {
		t.Error("portfolio workspace reported empty")
	}
	if !ws.AppTypes["portfolio"] {
		t.Errorf("AppTypes = %v, want portfolio", ws.AppTypes)
	}
}

func TestDetectWorkspaceEmptyAndMissing(t *testing.T) {
	ws := NewConflictDetector().Detect(emptyWorkspace(t))
	if !ws.Empty || len(ws.AppTypes) != 0 {
		t.Errorf("empty workspace state = %+v, want Empty", ws)
	}
	ws = NewConflictDetector().Detect(filepath.Join(t.TempDir(), "missing"))
	if !ws.Empty {
		t.Error("missing workspace must be empty")
	}
}

func TestConflictDetectorNegation(t *testing.T) {
	d := NewConflictDetector()
	ws := d.Detect(todoWorkspace(t))
	res := &Resolution{
		Category:   ir.CategoryRedesign,
		TargetType: "portfolio",
		Entities:   map[string]string{},
		Negated:    map[string]bool{"todo_app": true},
	}
	conf := d.Process(res, ws)
	if !conf.Present {
		t.Fatal("expected conflict for explicit negation over matching workspace")
	}
	if !strings.Contains(conf.Reason, "explicitly excludes todo_app") {
		t.Errorf("Reason = %q", conf.Reason)
	}
	if len(conf.Detected) != 1 || conf.Detected[0] != "todo_app" {
		t.Errorf("Detected = %v, want [todo_app]", conf.Detected)
	}
}

func TestConflictDetectorTargetMismatch(t *testing.T) {
	d := NewConflictDetector()
	ws := d.Detect(todoWorkspace(t))
	res := &Resolution{
		Category:   ir.CategoryCreate,
		TargetType: "portfolio",
		Entities:   map[string]string{},
		Negated:    map[string]bool{},
	}
	conf := d.Process(res, ws)
	if !conf.Present {
		t.Fatal("expected conflict for portfolio over todo workspace")
	}
	if !strings.Contains(conf.Reason, "over an existing todo_app") {
		t.Errorf("Reason = %q", conf.Reason)
	}
}

func TestConflictDetectorNoConflict(t *testing.T) {
	d := NewConflictDetector()

	// Matching target over the same type is not a conflict.
	ws := d.Detect(todoWorkspace(t))
	res := &Resolution{
		Category:   ir.CategoryRedesign,
		TargetType: "todo_app",
		Entities:   map[string]string{},
		Negated:    map[string]bool{},
	}
	if conf := d.Process(res, ws); conf.Present {
		t.Errorf("unexpected conflict: %+v", conf)
	}

	// Any request over an empty workspace is not a conflict.
	if conf := d.Process(res, d.Detect(emptyWorkspace(t))); conf.Present {
		t.Errorf("unexpected conflict on empty workspace: %+v", conf)
	}
}

// --- AmbiguityDetector ------------------------------------------------------

func TestAmbiguityDetector(t *testing.T) {
	a := NewAmbiguityDetector()
	ws := WorkspaceState{AppTypes: map[string]bool{"todo_app": true}}
	res := &Resolution{Category: ir.CategoryRedesign, TargetType: "portfolio", Entities: map[string]string{}, Negated: map[string]bool{}}

	conflict := Conflict{Present: true, Requested: "portfolio", Detected: []string{"todo_app"}, Reason: "requested portfolio over an existing todo_app workspace"}
	if !a.Process(res, ws, conflict) {
		t.Error("Process = false, want true on conflict")
	}
	qs := a.Questions(res, ws, conflict)
	if len(qs) != 1 {
		t.Fatalf("Questions = %d, want 1", len(qs))
	}
	q := qs[0]
	if q.ID == "" || q.Header == "" {
		t.Errorf("question ID/Header empty: %+v", q)
	}
	if !strings.Contains(q.QuestionText, "portfolio") || !strings.Contains(q.QuestionText, "todo_app") {
		t.Errorf("QuestionText = %q", q.QuestionText)
	}
	if len(q.Options) != 4 {
		t.Errorf("Options = %v, want 4 branches (replace, alongside, merge, custom)", q.Options)
	}
	if got := q.DefaultOptionID(); got != ir.OptionMergeSelective {
		t.Errorf("DefaultOptionID = %q, want merge_selective", got)
	}
	if q.Reason == "" {
		t.Error("Reason empty")
	}
	var hasCustom, hasReplace bool
	for _, o := range q.Options {
		if o.ID == ir.OptionTypeYourOwn {
			hasCustom = true
		}
		if o.ID == ir.OptionReplaceWorkspace {
			hasReplace = true
		}
	}
	if !hasCustom || !hasReplace {
		t.Errorf("options must include the custom and replace branches, got %+v", q.Options)
	}

	if a.Process(res, ws, Conflict{}) {
		t.Error("Process = true without conflict")
	}
	if qs := a.Questions(res, ws, Conflict{}); len(qs) != 0 {
		t.Errorf("Questions without conflict = %v, want nil", qs)
	}
}

// --- helpers ----------------------------------------------------------------

func TestSortedKeys(t *testing.T) {
	got := SortedKeys(map[string]bool{"portfolio": true, "todo_app": true})
	if len(got) != 2 || got[0] != "portfolio" || got[1] != "todo_app" {
		t.Errorf("SortedKeys = %v, want [portfolio todo_app]", got)
	}
}
