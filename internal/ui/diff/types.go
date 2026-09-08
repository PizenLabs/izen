package diff

// LineType classifies a single line inside a unified diff.
type LineType int

const (
	LineUnchanged LineType = iota
	LineAdded
	LineDeleted
	LineHunkHeader
	LineFileHeader
)

// DiffLine is one parsed diff line with precise source line numbers.
type DiffLine struct {
	Type  LineType
	OldNo int    // 0 when Type == LineAdded (no old-side line)
	NewNo int    // 0 when Type == LineDeleted (no new-side line)
	Text  string // Raw content without the leading +/-/space prefix
}

// DiffHunk is a single @@ ... @@ block within a file.
type DiffHunk struct {
	OldStart  int
	OldLength int
	NewStart  int
	NewLength int
	Header    string // Full header line, e.g. "@@ -12,7 +12,9 @@ func ProcessData() {"
	Lines     []DiffLine
	Collapsed bool // When true, long unchanged runs fold into a summary bar
}

// FileDiff is the parsed diff for a single file.
type FileDiff struct {
	OldPath string
	NewPath string
	Hunks   []DiffHunk
	IsNew   bool // created file (--- /dev/null)
	IsDel   bool // deleted file (+++ /dev/null)
}

// DisplayPath returns the most useful path for rendering.
func (f FileDiff) DisplayPath() string {
	if f.NewPath != "" && f.NewPath != "/dev/null" {
		return f.NewPath
	}
	return f.OldPath
}

// StatusLabel returns [MODIFIED], [CREATED], or [DELETED].
func (f FileDiff) StatusLabel() string {
	switch {
	case f.IsNew:
		return "[CREATED]"
	case f.IsDel:
		return "[DELETED]"
	default:
		return "[MODIFIED]"
	}
}
