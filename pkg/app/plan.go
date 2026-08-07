package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/pkg/capability"
	"github.com/PizenLabs/izen/pkg/event"
	"github.com/PizenLabs/izen/pkg/extractor"
	txfs "github.com/PizenLabs/izen/pkg/fs"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
	"github.com/PizenLabs/izen/pkg/knowledge"
	"github.com/PizenLabs/izen/pkg/op"
	"github.com/PizenLabs/izen/pkg/planner"
	"github.com/PizenLabs/izen/pkg/planner/brownfield"
	"github.com/PizenLabs/izen/pkg/planner/greenfield"
)

// Mode selects the planning strategy for a pipeline run.
type Mode string

// Supported planning modes.
const (
	// ModeAuto detects greenfield vs brownfield from the workspace.
	ModeAuto Mode = "auto"
	// ModeGreenfield plans a one-shot batch workspace write.
	ModeGreenfield Mode = "greenfield"
	// ModeBrownfield plans an interactive edit-and-verify graph.
	ModeBrownfield Mode = "brownfield"
)

// Detector reports whether the workspace root is an existing (brownfield)
// project that should route through the interactive planner.
type Detector func(root string) (brownfield bool, err error)

// Option configures a Pipeline.
type Option func(*Pipeline)

// WithRegistry overrides the capability registry. Default capabilities are
// registered into the provided registry when absent.
func WithRegistry(r *capability.Registry) Option {
	return func(p *Pipeline) {
		if r != nil {
			p.registry = r
		}
	}
}

// WithExtractors overrides the evidence-based extractor chain. Defaults to
// MarkdownFenceExtractor then JSONExtractor.
func WithExtractors(extractors ...extractor.Extractor) Option {
	return func(p *Pipeline) {
		if len(extractors) > 0 {
			p.extractors = append([]extractor.Extractor(nil), extractors...)
		}
	}
}

// WithGenerator binds the LLM generator the pipeline generates through. It is
// mandatory: Run fails with ErrNoGenerator when absent.
func WithGenerator(g Generator) Option {
	return func(p *Pipeline) {
		if g != nil {
			p.generator = g
		}
	}
}

// WithClarifier binds the interactive clarifier the pipeline blocks on when an
// ambiguous intent asks its questions. A TUI host wires the AskModel here;
// when absent, ambiguous intents auto-resolve to their default options so
// headless runs never hang.
func WithClarifier(c Clarifier) Option {
	return func(p *Pipeline) {
		if c != nil {
			p.clarifier = c
		}
	}
}

// WithEventBus overrides the shared event bus. A bus is created when absent.
func WithEventBus(bus *event.MemoryEventBus) Option {
	return func(p *Pipeline) {
		if bus != nil {
			p.bus = bus
		}
	}
}

// WithRoot sets the workspace root artifacts are planned into.
func WithRoot(root string) Option {
	return func(p *Pipeline) {
		if root != "" {
			p.root = root
		}
	}
}

// WithStrategyRegistry overrides the op.StrategyResolver registry used to
// compile a ContextPolicy from the intent semantics. Defaults to the registry
// of canonical resolvers.
func WithStrategyRegistry(r *op.StrategyRegistry) Option {
	return func(p *Pipeline) {
		if r != nil {
			p.strategy = r
		}
	}
}

// WithKnowledgeGraph overrides the shared RuntimeKnowledge graph used to list
// workspace files, strip obsolete contents under PolicyRewrite and inject
// bounded baseline context under PolicyEdit/PolicyPatch.
func WithKnowledgeGraph(kg *knowledge.KnowledgeGraph) Option {
	return func(p *Pipeline) {
		if kg != nil {
			p.kg = kg
		}
	}
}

// WithIntentCompiler wires the IntentCompiler the pipeline triggers when a
// request carries no compiled ir.IntentIR, so OperationSemantics always derives
// from a strongly-typed ir.Category. When absent, or when compilation fails,
// the pipeline uses the compiler package's deterministic headless fallback.
func WithIntentCompiler(c IntentCompiler) Option {
	return func(p *Pipeline) {
		if c != nil {
			p.compiler = c
		}
	}
}

// WithMode forces the planning strategy instead of auto-detecting it.
func WithMode(m Mode) Option {
	return func(p *Pipeline) {
		if m != "" {
			p.mode = m
		}
	}
}

// WithDetector overrides the greenfield/brownfield workspace detector.
func WithDetector(d Detector) Option {
	return func(p *Pipeline) {
		if d != nil {
			p.detect = d
		}
	}
}

// WithVerifyCommand overrides the brownfield verify-command builder. It
// receives the workspace root and returns the shell command run after an edit
// batch. Defaults to a toolchain-aware command derived from the workspace.
func WithVerifyCommand(builder func(root string) string) Option {
	return func(p *Pipeline) {
		if builder != nil {
			p.verify = builder
		}
	}
}

// WithModelTier selects the capability prompt verbosity tier ("full" or
// "small").
func WithModelTier(tier string) Option {
	return func(p *Pipeline) {
		if tier != "" {
			p.modelTier = tier
		}
	}
}

// WithMaxAttempts bounds the number of extraction retries before the pipeline
// fails.
func WithMaxAttempts(n int) Option {
	return func(p *Pipeline) {
		if n > 0 {
			p.maxAttempts = n
		}
	}
}

// WithMaxRepairs bounds the number of validation-gate repair rounds and the
// brownfield repair budget.
func WithMaxRepairs(n int) Option {
	return func(p *Pipeline) {
		if n > 0 {
			p.maxRepairs = n
		}
	}
}

// WithTxFS overrides the transactional file system artifact writes stage
// through. Defaults to a TxFS bound to the pipeline workspace root. The bound
// root must match the pipeline root; it is reused across runs.
func WithTxFS(tx *txfs.TxFS) Option {
	return func(p *Pipeline) {
		if tx != nil {
			p.tx = tx
		}
	}
}

// defaultDetector treats a workspace as brownfield when it contains any
// non-hidden file or directory beyond the empty greenfield case.
func defaultDetector(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if e.IsDir() || isManifest(name) || isSourceFile(name) {
			return true, nil
		}
	}
	return false, nil
}

// isManifest reports whether name is a recognized project manifest.
func isManifest(name string) bool {
	switch name {
	case "go.mod", "go.sum", "package.json", "Cargo.toml", "pyproject.toml",
		"requirements.txt", "pom.xml", "build.gradle", "Makefile", "Dockerfile",
		"composer.json", "Gemfile":
		return true
	}
	return false
}

// isSourceFile reports whether name carries a recognized source extension.
func isSourceFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".rb",
		".c", ".cpp", ".h", ".hpp", ".cs", ".php", ".html", ".htm", ".css",
		".scss", ".md", ".json", ".yml", ".yaml", ".toml", ".sh":
		return true
	}
	return false
}

// detectVerifyCommand derives a toolchain-aware verify command from the
// workspace contents.
func detectVerifyCommand(root string) string {
	switch {
	case fileExists(root, "go.mod"):
		return "go build ./... && go test ./..."
	case fileExists(root, "package.json"):
		return "npm run build"
	case fileExists(root, "Cargo.toml"):
		return "cargo build"
	case fileExists(root, "pyproject.toml"), fileExists(root, "requirements.txt"):
		return "python -m compileall -q ."
	case fileExists(root, "Makefile"):
		return "make"
	default:
		return brownfield.DefaultVerifyCommand("")
	}
}

// fileExists reports whether name is a regular file under root.
func fileExists(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && !info.IsDir()
}

// plan lowers the validated artifacts into an ExecutionGraph, selecting the
// greenfield or brownfield planner. Parent directories are created here,
// strictly after the validation gate, so no unvalidated content ever reaches
// disk.
//
// Execution modes derive strictly from the compiled ContextPolicy: a
// PolicyRewrite (total replacement) forces the one-shot greenfield planner,
// because keeping the brownfield repair loop would contradict the user's
// explicit replacement. A compiled intent that resolved to PreserveWorkspace
// false does the same.
func (p *Pipeline) plan(ctx context.Context, intent string, artifacts []ir.Artifact, intentIR *ir.IntentIR, policy op.ContextPolicy) (*planner.PlanResult, Mode, *brownfield.BrownfieldPlanner, error) {
	useBrownfield := p.mode == ModeBrownfield
	if p.mode == ModeAuto {
		bf, err := p.detect(p.root)
		if err != nil {
			return nil, ModeAuto, nil, fmt.Errorf("detect workspace: %w", err)
		}
		useBrownfield = bf
	}
	if policy == op.PolicyRewrite || (intentIR != nil && !intentIR.PreserveWorkspace) {
		useBrownfield = false
	}

	// Reject any artifact path that escapes the workspace root before a single
	// directory is created or byte is written.
	if err := validateArtifactPaths(p.root, artifacts); err != nil {
		return nil, ModeAuto, nil, err
	}

	if useBrownfield {
		// Brownfield writes and its verify command run against the live
		// workspace, so parent directories are created eagerly here. The
		// greenfield path defers directory creation to TxFS.Commit, letting a
		// failed run roll the workspace back to a completely pristine state.
		if err := ensureParentDirs(p.root, artifacts); err != nil {
			return nil, ModeAuto, nil, err
		}
		verify := p.verify
		if verify == nil {
			verify = detectVerifyCommand
		}
		bf, err := brownfield.NewBrownfieldPlanner(
			p.root,
			brownfield.WithVerifyCommand(func(string) string { return verify(p.root) }),
			brownfield.WithTimeout(2*time.Minute),
		)
		if err != nil {
			return nil, ModeBrownfield, nil, err
		}
		res, err := bf.Plan(ctx, intent, artifacts)
		if err != nil {
			return nil, ModeBrownfield, nil, err
		}
		return res, ModeBrownfield, bf, nil
	}

	gf := greenfield.NewGreenfieldPlanner(p.root, greenfield.WithTxFS(p.tx))
	res, err := gf.Plan(ctx, intent, artifacts)
	if err != nil {
		return nil, ModeGreenfield, nil, err
	}
	return res, ModeGreenfield, nil, nil
}

// execute runs the planned graph on the kernel engine, dispatching side
// effects exclusively through the shared event bus. Before any file is
// touched, the plan's artifact writes are sequenced through the execution DAG
// and checked for cyclic inter-file dependencies. Brownfield graphs run
// through the closed-loop repair loop; greenfield writes stage through the
// pipeline's TxFS transaction and reach disk only at Commit.
func (p *Pipeline) execute(ctx context.Context, planResult *planner.PlanResult, mode Mode, bf *brownfield.BrownfieldPlanner) error {
	if planResult == nil || planResult.Graph == nil {
		return errors.New("app: plan produced no graph")
	}
	// Sequence the artifact application through the DAG topological order and
	// reject any cyclic dependency before a single file is written.
	if _, err := planExecutionOrder(planResult.Artifacts); err != nil {
		return fmt.Errorf("app: execution order: %w", err)
	}
	engine := kernel.NewEngine(p.bus)
	if mode == ModeBrownfield && bf != nil {
		return bf.ExecuteAndRepair(ctx, engine, planResult.Graph, p.maxRepairs)
	}
	_, err := planResult.Graph.Execute(ctx, engine)
	return err
}
