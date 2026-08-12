package changeset

// CompiledChange pairs a ChangeSet with its authoritative compiled diff and the
// Patch Validator's verdict.
type CompiledChange struct {
	ChangeSet  ChangeSet
	Diff       []byte
	Validation ValidationReport
}

// Pipeline is the strict execution chain from the architecture spec:
//
//	MODEL OUTPUT → Output Normalizer → Change Extractor → ChangeSet IR
//	             → Diff Compiler → Patch Validator
//
// It is read-only: it never touches the filesystem. Applying the returned diffs
// is the caller's responsibility (through the patch engine or the build
// executor's mutation pipeline).
type Pipeline struct {
	Compiler  *Compiler
	Validator *Validator
}

// NewPipeline wires the default Diff Compiler and Patch Validator.
func NewPipeline() *Pipeline {
	return &Pipeline{Compiler: NewCompiler(), Validator: NewValidator()}
}

// Run executes the full chain over a single target file. It returns
// ErrAmbiguousChange when model output cannot be mapped safely onto the target
// file; in that case the pipeline is PAUSED and no change is compiled.
func (p *Pipeline) Run(output string, targetFile string, originalDiskContent []byte) ([]CompiledChange, error) {
	css, err := Extract(output, targetFile, originalDiskContent)
	if err != nil {
		return nil, err
	}
	out := make([]CompiledChange, 0, len(css))
	for _, cs := range css {
		diff, err := p.Compiler.CompileToPatch(cs, originalDiskContent)
		if err != nil {
			return nil, err
		}
		out = append(out, CompiledChange{
			ChangeSet:  cs,
			Diff:       diff,
			Validation: p.Validator.Validate(diff),
		})
	}
	return out, nil
}
