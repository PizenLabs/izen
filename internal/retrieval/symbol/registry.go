package symbol

type ExtractorRegistry struct {
	extractors []LanguageExtractor
}

func NewExtractorRegistry(extractors ...LanguageExtractor) *ExtractorRegistry {
	return &ExtractorRegistry{
		extractors: extractors,
	}
}

func (r *ExtractorRegistry) DetectLanguage(rootPath string) (LanguageID, LanguageExtractor, bool) {
	for _, ext := range r.extractors {
		lang, ok := ext.DetectLanguage(rootPath)
		if ok {
			return lang, ext, true
		}
	}
	return "", nil, false
}

func (r *ExtractorRegistry) GetAllExtractors() []LanguageExtractor {
	return r.extractors
}

func (r *ExtractorRegistry) Languages() []LanguageID {
	var langs []LanguageID
	for _, ext := range r.extractors {
		if lang, ok := ext.DetectLanguage(""); ok {
			_ = lang
			_ = ok
		}
	}
	return langs
}
