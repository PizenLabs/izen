package layer3

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRenameFunctionAcrossFiles(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	pr, err := h.RenameSymbol(context.Background(), RenameRequest{Name: "Compute", NewName: "ComputeAll"})
	if err != nil {
		t.Fatalf("RenameSymbol: %v", err)
	}
	if !pr.Changed {
		t.Fatal("expected changes")
	}
	if !containsStr(pr.Paths(), "svc/service.go") || !containsStr(pr.Paths(), "cmd/app/main.go") {
		t.Fatalf("unexpected scope: %v", pr.Paths())
	}

	svc := patchFor(t, pr, "svc/service.go")
	if !strings.Contains(svc.New, "func ComputeAll(n int) int") {
		t.Errorf("declaration not renamed:\n%s", svc.New)
	}
	if !strings.Contains(svc.New, "Compute := 5") {
		t.Errorf("shadowed local must keep its name:\n%s", svc.New)
	}
	if strings.Contains(svc.New, "func Compute(n int)") {
		t.Errorf("old declaration still present:\n%s", svc.New)
	}

	main := patchFor(t, pr, "cmd/app/main.go")
	if !strings.Contains(main.New, "svc.ComputeAll(2)") {
		t.Errorf("package-qualified call not renamed:\n%s", main.New)
	}
	if strings.Contains(main.New, "svc.Compute(") {
		t.Errorf("old call still present:\n%s", main.New)
	}
}

func TestRenameMethodWithInterface(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	pr, err := h.RenameSymbol(context.Background(), RenameRequest{Name: "Run", QualName: "Service.Run", NewName: "Execute"})
	if err != nil {
		t.Fatalf("RenameSymbol: %v", err)
	}
	if !pr.Changed {
		t.Fatal("expected changes")
	}

	svc := patchFor(t, pr, "svc/service.go")
	if !strings.Contains(svc.New, "func (s *Service) Execute() error") {
		t.Errorf("method declaration not renamed:\n%s", svc.New)
	}
	if !strings.Contains(svc.New, "type Runner interface") || !strings.Contains(svc.New, "Execute() error") {
		t.Errorf("interface method not renamed:\n%s", svc.New)
	}
	if !strings.Contains(svc.New, "s.helper()") {
		t.Errorf("unrelated method call was altered:\n%s", svc.New)
	}

	main := patchFor(t, pr, "cmd/app/main.go")
	if !strings.Contains(main.New, "s.Execute()") {
		t.Errorf("call site not renamed:\n%s", main.New)
	}
	if strings.Contains(main.New, "s.Run()") {
		t.Errorf("old call site still present:\n%s", main.New)
	}
}

func TestRenameTypeScriptFunction(t *testing.T) {
	h, _ := newTestHandler(t, tsFixture())
	pr, err := h.RenameSymbol(context.Background(), RenameRequest{Name: "createServer", NewName: "buildServer"})
	if err != nil {
		t.Fatalf("RenameSymbol: %v", err)
	}
	p := patchFor(t, pr, "web/server.ts")
	if !strings.Contains(p.New, "export function buildServer(): Server {") {
		t.Errorf("TS function not renamed:\n%s", p.New)
	}
	if strings.Contains(p.New, "createServer") {
		t.Errorf("old name still present:\n%s", p.New)
	}
	if !strings.Contains(p.New, `import { thing } from "./dep";`) {
		t.Errorf("string literal was altered:\n%s", p.New)
	}
}

func TestRenameWithExplicitScope(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	pr, err := h.RenameSymbol(context.Background(), RenameRequest{
		Name:    "Compute",
		NewName: "ComputeAll",
		Paths:   []string{"cmd/app/main.go"},
	})
	if err != nil {
		t.Fatalf("RenameSymbol: %v", err)
	}
	// The declaration file is always included; the explicit scope restricts
	// the reference files to the requested paths.
	if !containsStr(pr.Paths(), "cmd/app/main.go") {
		t.Fatalf("explicit scope missing: %v", pr.Paths())
	}
	if !containsStr(pr.Paths(), "svc/service.go") {
		t.Fatalf("declaration file missing from scope: %v", pr.Paths())
	}
	if len(pr.Paths()) != 2 {
		t.Fatalf("unexpected scope: %v", pr.Paths())
	}
}

func TestRenameErrors(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	ctx := context.Background()

	if _, err := h.RenameSymbol(ctx, RenameRequest{Name: "Nope", NewName: "X"}); !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("missing symbol err = %v", err)
	}
	if _, err := h.RenameSymbol(ctx, RenameRequest{Name: "Compute", NewName: "Compute"}); !errors.Is(err, ErrRenamedSameName) {
		t.Errorf("same-name err = %v", err)
	}
	if _, err := h.RenameSymbol(ctx, RenameRequest{Name: "", NewName: "X"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty name err = %v", err)
	}
}

func TestFormatGo(t *testing.T) {
	h, _ := newTestHandler(t, goFormatFixture())
	pr, err := h.FormatFile(context.Background(), "svc/messy.go")
	if err != nil {
		t.Fatalf("FormatFile: %v", err)
	}
	if !pr.Changed || len(pr.Files) != 1 {
		t.Fatalf("unexpected result: %+v", pr)
	}
	new := pr.Files[0].New
	if !strings.Contains(new, "func Messy(a int) int {") || !strings.Contains(new, "return a + 1") {
		t.Errorf("not formatted:\n%s", new)
	}
	if pr.Ops[0].Type != OpFormat {
		t.Errorf("op type = %v, want format", pr.Ops[0].Type)
	}
}

func TestFormatAlreadyFormattedIsSkipped(t *testing.T) {
	h, _ := newTestHandler(t, goFormatFixture())
	pr, err := h.FormatFile(context.Background(), "svc/unused.go")
	if err != nil {
		t.Fatalf("FormatFile: %v", err)
	}
	if pr.Changed {
		t.Error("already-formatted file must not report changes")
	}
	if pr.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", pr.Skipped)
	}
}

func TestFormatTSWithFormatter(t *testing.T) {
	called := false
	formatter := func(ctx context.Context, lang, path string, src []byte) ([]byte, error) {
		called = true
		return []byte(strings.ToUpper(string(src))), nil
	}
	h, _ := newTestHandler(t, tsFixture(), WithFormatter(formatter))
	pr, err := h.FormatFile(context.Background(), "web/server.ts")
	if err != nil {
		t.Fatalf("FormatFile: %v", err)
	}
	if !called {
		t.Error("formatter was not invoked")
	}
	if !pr.Changed || !strings.Contains(pr.Files[0].New, "EXPORT INTERFACE CONFIG") {
		t.Errorf("formatter output not applied: %+v", pr.Files[0].New[:40])
	}
}

func TestFormatTSWithoutFormatter(t *testing.T) {
	h, _ := newTestHandler(t, tsFixture())
	_, err := h.FormatFile(context.Background(), "web/server.ts")
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestAddImport(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	ctx := context.Background()

	pr, err := h.AddImport(ctx, "svc/service.go", "os")
	if err != nil {
		t.Fatalf("AddImport: %v", err)
	}
	if !pr.Changed {
		t.Fatal("expected change")
	}
	new := pr.Files[0].New
	if !strings.Contains(new, `"os"`) || !strings.Contains(new, `"fmt"`) {
		t.Errorf("import not inserted:\n%s", new)
	}
	if pr.Ops[0].Type != OpAddImport {
		t.Errorf("op type = %v, want add_import", pr.Ops[0].Type)
	}

	// Duplicate import is a no-op.
	dup, err := h.AddImport(ctx, "svc/service.go", "fmt")
	if err != nil {
		t.Fatalf("AddImport duplicate: %v", err)
	}
	if dup.Changed {
		t.Error("duplicate import must not change the file")
	}
	if dup.Skipped != 1 {
		t.Errorf("duplicate skipped = %d, want 1", dup.Skipped)
	}
}

func TestAddImportCreatesImportDecl(t *testing.T) {
	h, _ := newTestHandler(t, tsFixture())
	pr, err := h.AddImport(context.Background(), "web/dep.ts", "node:path")
	if err != nil {
		t.Fatalf("AddImport: %v", err)
	}
	if !pr.Changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(pr.Files[0].New, `import "node:path";`) {
		t.Errorf("import not inserted:\n%s", pr.Files[0].New)
	}
}

func TestRemoveUnusedImports(t *testing.T) {
	h, _ := newTestHandler(t, goFormatFixture())
	pr, err := h.RemoveUnusedImports(context.Background(), "svc/unused.go")
	if err != nil {
		t.Fatalf("RemoveUnusedImports: %v", err)
	}
	if !pr.Changed {
		t.Fatal("expected change")
	}
	new := pr.Files[0].New
	if strings.Contains(new, `"strings"`) {
		t.Errorf("unused import not removed:\n%s", new)
	}
	if !strings.Contains(new, `"fmt"`) {
		t.Errorf("used import removed:\n%s", new)
	}
	if pr.Ops[0].Type != OpRemoveImport {
		t.Errorf("op type = %v, want remove_import", pr.Ops[0].Type)
	}
}

func TestHandleDispatch(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	ctx := context.Background()

	pr, err := h.Handle(ctx, Request{Intent: IntentRename, TargetSymbol: "Compute", NewName: "ComputeAll"})
	if err != nil {
		t.Fatalf("Handle rename: %v", err)
	}
	if !pr.Changed {
		t.Error("rename via Handle did not change anything")
	}

	pr, err = h.Handle(ctx, Request{Intent: IntentFormat, TargetFile: "svc/service.go"})
	if err != nil {
		t.Fatalf("Handle format: %v", err)
	}
	if pr.Changed {
		t.Error("already-formatted file changed")
	}

	if _, err := h.Handle(ctx, Request{Intent: IntentRefactor}); !errors.Is(err, ErrUnsupportedIntent) {
		t.Errorf("generative intent err = %v, want ErrUnsupportedIntent", err)
	}
}

func TestApplyPatches(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	root := h.Sor().Root()
	pr, err := h.RenameSymbol(context.Background(), RenameRequest{Name: "Compute", NewName: "ComputeAll"})
	if err != nil {
		t.Fatalf("RenameSymbol: %v", err)
	}
	res, err := ApplyPatches(context.Background(), root, pr.Files)
	if err != nil {
		t.Fatalf("ApplyPatches: %v", err)
	}
	if res.Applied != len(pr.Files) {
		t.Errorf("applied = %d, want %d", res.Applied, len(pr.Files))
	}
	svc, err := os.ReadFile(filepath.Join(root, "svc/service.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(svc), "func ComputeAll(n int) int") {
		t.Errorf("patched file missing rename:\n%s", svc)
	}
}

func TestApplyPatchesRejectsEscape(t *testing.T) {
	_, err := ApplyPatches(context.Background(), t.TempDir(), []FilePatch{{
		Path:    "../escape.go",
		Changed: true,
		New:     "package escape",
	}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestRenameConcurrent(t *testing.T) {
	h, _ := newTestHandler(t, goFixture())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pr, err := h.RenameSymbol(context.Background(), RenameRequest{Name: "Compute", NewName: "ComputeAll"})
			if err != nil {
				t.Errorf("RenameSymbol: %v", err)
				return
			}
			if !pr.Changed {
				t.Error("expected changes")
			}
		}()
	}
	wg.Wait()
}
