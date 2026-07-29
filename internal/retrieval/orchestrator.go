package retrieval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/retrieval/fulltext"
)

type PipelineStage int

const (
	StageGraph    PipelineStage = 0
	StageVector   PipelineStage = 1
	StageFullText PipelineStage = 2
	StageRipgrep  PipelineStage = 3
)

func (s PipelineStage) String() string {
	switch s {
	case StageGraph:
		return "graph"
	case StageVector:
		return "vector_rag"
	case StageFullText:
		return "fulltext"
	case StageRipgrep:
		return "ripgrep"
	default:
		return "unknown"
	}
}

func (s PipelineStage) ConfidenceWeight() float64 {
	switch s {
	case StageGraph:
		return ConfExact.Float64()
	case StageVector:
		return ConfSemantic.Float64()
	case StageFullText:
		return ConfPattern.Float64()
	case StageRipgrep:
		return ConfText.Float64()
	default:
		return ConfFallback.Float64()
	}
}

type VectorRAGProvider interface {
	Search(ctx context.Context, query string) (*ResultSet, error)
}

type FullTextIndex interface {
	Search(ctx context.Context, query string, opts fulltext.SearchOptions) ([]fulltext.Match, error)
	IndexFile(path string) bool
	IndexWorkspace(ctx context.Context) (int, error)
	RefreshIndex(ctx context.Context) (int, error)
	DocCount() int
	Stats() fulltext.Stats
}

type PipelineResult struct {
	ResultSet     *ResultSet
	LayersUsed    []PipelineStage
	StopReason    string
	TotalDuration time.Duration
	TokenEstimate int
	TokenBudget   int
	Truncated     bool
}

type OrchestratorConfig struct {
	GraphLookup    *GraphLookup
	Router         *Router
	FullText       FullTextIndex
	Fallback       *FallbackChain
	TokenEstimator *TokenWeightEstimator
	MaxTokens      int
	LogFn          func(string, ...interface{})
	AutoIndex      bool
}

type Orchestrator struct {
	graph          *GraphLookup
	router         *Router
	fulltext       FullTextIndex
	fallback       *FallbackChain
	tokenEstimator *TokenWeightEstimator
	maxTokens      int
	logFn          func(string, ...interface{})
}

func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	o := &Orchestrator{
		graph:          cfg.GraphLookup,
		router:         cfg.Router,
		fulltext:       cfg.FullText,
		fallback:       cfg.Fallback,
		tokenEstimator: cfg.TokenEstimator,
		maxTokens:      cfg.MaxTokens,
		logFn:          cfg.LogFn,
	}
	if o.tokenEstimator == nil {
		o.tokenEstimator = NewTokenWeightEstimator()
	}
	if o.maxTokens <= 0 {
		o.maxTokens = 8000
	}
	if o.logFn == nil {
		o.logFn = func(string, ...interface{}) {}
	}

	if o.fulltext != nil && cfg.AutoIndex {
		ctx := context.Background()
		if n, err := o.fulltext.IndexWorkspace(ctx); err == nil && n > 0 {
			o.log("[orchestrator] auto-indexed %d files into fulltext engine", n)
		}
	}

	return o
}

func (o *Orchestrator) Execute(ctx context.Context, query Query) (*PipelineResult, error) {
	start := time.Now()

	result := &ResultSet{Strategy: "orchestrator.pipeline"}
	layersUsed := []PipelineStage{}
	stopReason := "completed"
	totalTokens := 0

	for stage := StageGraph; stage <= StageRipgrep; stage++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		stageResult := o.executeStage(ctx, stage, query)
		if stageResult == nil || stageResult.Empty() {
			if stageResult != nil && stageResult.Error != "" {
				o.log("stage %s: %s", stage, stageResult.Error)
			} else {
				o.log("[Retrieval] %s miss -> escalating to %s", stage, nextStageLabel(stage))
			}
			continue
		}

		layersUsed = append(layersUsed, stage)
		result.Merge(stageResult)

		contentBudget := o.tokenEstimator.Estimate(resultSetContent(result))
		totalTokens += contentBudget
		if totalTokens > o.maxTokens {
			o.log("[Retrieval] token budget exceeded (%d > %d), truncating", totalTokens, o.maxTokens)
			break
		}

		best := stageResult.Best()
		effScore := best.Score
		if effScore == 0 {
			effScore = best.Confidence
		}
		if effScore >= 0.7 || stageResult.Confidence >= ConfExact.Float64() {
			o.log("[Retrieval] %s hit confidence %.3f — pipeline stopped", stage, effScore)
			stopReason = fmt.Sprintf("stage %s confidence %.3f >= 0.7", stage, effScore)
			break
		}

		o.log("[Retrieval] %s confidence %.3f < 0.7 — continuing", stage, effScore)
	}

	result.Duration = time.Since(start).Round(time.Millisecond).String()
	if result.Strategy == "orchestrator.pipeline" {
		result.Strategy = fmt.Sprintf("orchestrator.{%s}", stageList(layersUsed))
	}

	truncated := false
	if totalTokens > o.maxTokens && len(result.Results) > 0 {
		truncated = true
		o.truncateResults(result, o.maxTokens)
	}

	return &PipelineResult{
		ResultSet:     result,
		LayersUsed:    layersUsed,
		StopReason:    stopReason,
		TotalDuration: time.Since(start).Round(time.Millisecond),
		TokenEstimate: totalTokens,
		TokenBudget:   o.maxTokens,
		Truncated:     truncated,
	}, nil
}

func (o *Orchestrator) executeStage(ctx context.Context, stage PipelineStage, query Query) *ResultSet {
	switch stage {
	case StageGraph:
		return o.executeGraphStage(query)
	case StageVector:
		return o.executeVectorStage(ctx, query)
	case StageFullText:
		return o.executeFullTextStage(ctx, query)
	case StageRipgrep:
		return o.executeRipgrepStage(ctx, query)
	default:
		return nil
	}
}

func (o *Orchestrator) executeGraphStage(query Query) *ResultSet {
	if o.graph == nil || !o.graph.HasGraph() {
		return nil
	}

	switch {
	case query.Symbol != "":
		return o.graph.SearchAll(query.Symbol)
	case query.File != "":
		return o.graph.SearchFile(query.File)
	case query.Package != "":
		return o.graph.SearchPackage(query.Package)
	case query.Text != "":
		symResult := o.graph.SearchAll(query.Text)
		if !symResult.Empty() {
			return symResult
		}
		return o.graph.SearchImports(query.Text)
	default:
		return nil
	}
}

func (o *Orchestrator) executeVectorStage(ctx context.Context, query Query) *ResultSet {
	if o.router == nil || o.router.Engine() == nil {
		return nil
	}

	if query.Symbol != "" {
		o.log("[orchestrator] vector_rag resolve symbol: %s", query.Symbol)
		coords, err := o.router.ResolveSymbol(ctx, query.Symbol)
		if err != nil {
			o.log("[orchestrator] vector_rag resolve error: %v", err)
			return nil
		}
		rs := SearchResultAdapter(coords, "vector.resolve")
		if rs.Empty() {
			return nil
		}
		return rs
	}

	if query.Text == "" || len(query.Text) < 5 {
		return nil
	}

	o.log("[orchestrator] vector_rag search: %s", query.Text)
	chunks, err := o.router.SearchContext(ctx, query.Text)
	if err != nil {
		o.log("[orchestrator] vector_rag search error: %v", err)
		return nil
	}

	rs := SearchChunkAdapter(chunks, "vector.semantic")
	if rs.Empty() {
		return nil
	}
	return rs
}

func (o *Orchestrator) executeFullTextStage(ctx context.Context, query Query) *ResultSet {
	if o.fulltext == nil {
		return nil
	}

	searchText := query.Text
	if searchText == "" && query.Symbol != "" {
		searchText = query.Symbol
	}
	if searchText == "" {
		return nil
	}

	opts := fulltext.DefaultSearchOptions()
	opts.MaxResults = 30

	matches, err := o.fulltext.Search(ctx, searchText, opts)
	if err != nil {
		o.log("[orchestrator] fulltext search error: %v", err)
		return nil
	}
	if len(matches) == 0 {
		o.log("[orchestrator] fulltext search miss: %q", searchText)
		return nil
	}

	rs := &ResultSet{Strategy: "fulltext.match"}
	for _, m := range matches {
		rs.Add(Result{
			File:       m.Path,
			Line:       m.Line,
			Confidence: m.Score,
			Strategy:   "fulltext.match",
			Content:    m.Content,
			Score:      m.Score,
		})
	}

	if !rs.Empty() {
		rs.Confidence = rs.Results[0].Confidence
	}

	return rs
}

func (o *Orchestrator) executeRipgrepStage(ctx context.Context, query Query) *ResultSet {
	if o.fallback == nil || query.Text == "" {
		return nil
	}

	rs := o.fallback.Ripgrep(query.Text, query.FilePattern) //nolint:contextcheck
	if rs.Empty() {
		o.log("[Retrieval] ripgrep miss: %q", query.Text)
		return nil
	}
	_ = ctx
	return rs
}

func (o *Orchestrator) truncateResults(rs *ResultSet, budget int) {
	if rs == nil || rs.Empty() {
		return
	}

	total := 0
	keep := make([]Result, 0, len(rs.Results))
	for _, r := range rs.Results {
		est := o.tokenEstimator.Estimate(r.Content)
		if total+est > budget {
			break
		}
		total += est
		keep = append(keep, r)
	}
	rs.Results = keep
}

func (o *Orchestrator) log(format string, args ...interface{}) {
	o.logFn("[orchestrator] "+format, args...)
}

func nextStageLabel(current PipelineStage) string {
	next := current + 1
	if next > StageRipgrep {
		return "done"
	}
	return next.String()
}

func resultSetContent(rs *ResultSet) string {
	var b strings.Builder
	for _, r := range rs.Results {
		b.WriteString(r.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func stageList(stages []PipelineStage) string {
	labels := make([]string, len(stages))
	for i, s := range stages {
		labels[i] = s.String()
	}
	return strings.Join(labels, ",")
}

type OrchestratorOption func(*OrchestratorConfig)

func WithOrchestratorMaxTokens(n int) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.MaxTokens = n
	}
}

func WithOrchestratorLogFn(fn func(string, ...interface{})) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.LogFn = fn
	}
}

func WithOrchestratorGraphLookup(gl *GraphLookup) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.GraphLookup = gl
	}
}

func WithOrchestratorRouter(r *Router) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.Router = r
	}
}

func WithOrchestratorFullText(ft FullTextIndex) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.FullText = ft
	}
}

func WithOrchestratorFallback(fc *FallbackChain) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.Fallback = fc
	}
}

func WithOrchestratorAutoIndex(enabled bool) OrchestratorOption {
	return func(cfg *OrchestratorConfig) {
		cfg.AutoIndex = enabled
	}
}

func NewDefaultOrchestrator(root string, opts ...OrchestratorOption) *Orchestrator {
	cfg := &OrchestratorConfig{
		MaxTokens: 8000,
		LogFn:     globalActivityLog,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.FullText == nil {
		cfg.FullText = fulltext.NewEngine(root, fulltext.WithLogFn(cfg.LogFn))
		cfg.AutoIndex = true
	}

	return NewOrchestrator(*cfg)
}
