package retrieval

type Confidence int

const (
	ConfExact    Confidence = 100
	ConfFuzzy    Confidence = 85
	ConfPartial  Confidence = 70
	ConfSemantic Confidence = 60
	ConfPattern  Confidence = 45
	ConfText     Confidence = 30
	ConfFallback Confidence = 20
	ConfUnknown  Confidence = 0
)

func (c Confidence) Float64() float64 {
	return float64(c) / 100.0
}

func (c Confidence) Label() string {
	switch {
	case c >= 90:
		return "exact"
	case c >= 75:
		return "high"
	case c >= 55:
		return "medium"
	case c >= 35:
		return "low"
	default:
		return "fallback"
	}
}

func ConfidenceFromStrategy(strategy string) Confidence {
	switch strategy {
	case "graph.exact":
		return ConfExact
	case "graph.fuzzy":
		return ConfFuzzy
	case "graph.imports":
		return ConfPartial
	case "lynx.semantic":
		return ConfSemantic
	case "rg.pattern":
		return ConfPattern
	case "grep.text":
		return ConfText
	case "glob.file":
		return ConfPartial
	case "read.file":
		return ConfText
	default:
		return ConfUnknown
	}
}

type ScoredResult struct {
	Result     Result
	Confidence Confidence
}

func Score(confidence Confidence, result Result) Result {
	result.Confidence = confidence.Float64()
	return result
}