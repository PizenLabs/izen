package retrieval

import (
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
	"github.com/PizenLabs/izen/internal/retrieval/symbol/extractors"
)

func NewPolyglotRegistry() *symbol.ExtractorRegistry {
	return symbol.NewExtractorRegistry(
		extractors.NewGoExtractor(),
		extractors.NewJavaExtractor(),
		extractors.NewTSExtractor(),
		extractors.NewPythonExtractor(),
		extractors.NewRustExtractor(),
		extractors.NewCCExtractor(),
	)
}
