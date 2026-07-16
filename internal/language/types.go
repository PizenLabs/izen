package language

type ID string

type Category string

const (
	CategoryLanguage    Category = "language"
	CategoryFramework   Category = "framework"
	CategoryBuildSystem Category = "build_system"
)

type Def struct {
	ID              ID       `json:"id"`
	Name            string   `json:"name"`
	Category        Category `json:"category"`
	Extensions      []string `json:"extensions,omitempty"`
	IndicatorFiles  []string `json:"indicator_files,omitempty"`
	CommentSyntax   string   `json:"comment_syntax,omitempty"`
	HasTreeSitter   bool     `json:"has_tree_sitter"`
	TreeSitterPkg   string   `json:"tree_sitter_pkg,omitempty"`
	TreeSitterFunc  string   `json:"tree_sitter_func,omitempty"`
	Verification    Verifier `json:"verification,omitempty"`
	BuildIndicators []string `json:"build_indicators,omitempty"`
}

type Verifier struct {
	Fmt   []string `json:"fmt,omitempty"`
	Lint  []string `json:"lint,omitempty"`
	Vet   []string `json:"vet,omitempty"`
	Build []string `json:"build,omitempty"`
	Test  []string `json:"test,omitempty"`
}

type Detected struct {
	Def    *Def    `json:"def"`
	Weight float64 `json:"weight"`
}

var DefaultVerify = Verifier{}

const UnknownID ID = "_unknown"
