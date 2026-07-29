package fulltext

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Document struct {
	Path    string
	Size    int64
	ModTime time.Time
	Lang    string
}

type Posting struct {
	DocID     string
	Frequency int
	Positions []int
}

type PostingList []Posting

func (pl PostingList) Less(i, j int) bool {
	if pl[i].Frequency != pl[j].Frequency {
		return pl[i].Frequency > pl[j].Frequency
	}
	return pl[i].DocID < pl[j].DocID
}

type Match struct {
	Path    string  `json:"path"`
	Line    int     `json:"line"`
	Column  int     `json:"column"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

type SearchOptions struct {
	Exact      bool
	Phrase     bool
	Fuzzy      bool
	MaxErrors  int
	MaxResults int
}

func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		MaxErrors:  1,
		MaxResults: 50,
	}
}

type Engine struct {
	root  string
	mu    sync.RWMutex
	docs  map[string]*Document
	index map[string]PostingList
	logFn func(string, ...interface{})
}

type Option func(*Engine)

func WithLogFn(logFn func(string, ...interface{})) Option {
	return func(e *Engine) {
		if logFn != nil {
			e.logFn = logFn
		}
	}
}

func NewEngine(root string, opts ...Option) *Engine {
	e := &Engine{
		root:  root,
		docs:  make(map[string]*Document),
		index: make(map[string]PostingList),
		logFn: func(string, ...interface{}) {},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func (e *Engine) IndexFile(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	fullPath := filepath.Join(e.root, path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if !shouldIndex(path) {
		return false
	}

	existing, ok := e.docs[path]
	if ok && !info.ModTime().After(existing.ModTime) {
		return false
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false
	}

	content := string(data)
	doc := &Document{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Lang:    detectLang(path),
	}

	e.removeDoc(path)
	e.addDoc(path, doc, content)
	return true
}

func (e *Engine) IndexWorkspace(ctx context.Context) (int, error) {
	count := 0
	walkErr := filepath.Walk(e.root, func(path string, info os.FileInfo, walkFnErr error) error {
		if walkFnErr != nil {
			return walkFnErr
		}
		if info.IsDir() {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rel, relErr := filepath.Rel(e.root, path)
		if relErr != nil {
			return nil //nolint:nilerr
		}
		if e.IndexFile(rel) {
			count++
		}
		return nil
	})
	if walkErr != nil {
		return count, fmt.Errorf("index workspace: %w", walkErr)
	}
	return count, nil
}

func (e *Engine) RefreshIndex(ctx context.Context) (int, error) {
	count := 0
	walkErr := filepath.Walk(e.root, func(path string, info os.FileInfo, walkFnErr error) error {
		if walkFnErr != nil {
			return walkFnErr
		}
		if info.IsDir() {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rel, relErr := filepath.Rel(e.root, path)
		if relErr != nil {
			return nil //nolint:nilerr
		}

		existing, ok := e.docs[rel]
		if ok && !info.ModTime().After(existing.ModTime) {
			return nil
		}

		if e.IndexFile(rel) {
			count++
		}
		return nil
	})
	if walkErr != nil {
		return count, fmt.Errorf("refresh index: %w", walkErr)
	}
	return count, nil
}

func (e *Engine) Search(ctx context.Context, query string, opts SearchOptions) ([]Match, error) {
	if query == "" {
		return nil, nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	if len(e.index) == 0 {
		return nil, nil
	}

	var matches []Match
	seen := make(map[string]bool)

	switch {
	case opts.Phrase && len(tokens) > 1:
		matches = e.searchPhrase(tokens, &seen)
	case opts.Fuzzy:
		matches = e.searchFuzzy(query, tokens, opts.MaxErrors, &seen)
		if len(matches) < opts.MaxResults && len(tokens) > 0 {
			exactMatches := e.searchTokens(tokens, !opts.Exact, &seen)
			matches = append(matches, exactMatches...)
		}
	case opts.Exact:
		matches = e.searchExact(query, &seen)
	default:
		matches = e.searchTokens(tokens, true, &seen)
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Path < matches[j].Path
	})

	if opts.MaxResults > 0 && len(matches) > opts.MaxResults {
		matches = matches[:opts.MaxResults]
	}

	return matches, nil
}

func (e *Engine) DocCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.docs)
}

func (e *Engine) TokenCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.index)
}

func (e *Engine) searchExact(query string, seen *map[string]bool) []Match {
	postings, ok := e.index[strings.ToLower(strings.TrimSpace(query))]
	if !ok || len(postings) == 0 {
		return nil
	}

	var matches []Match
	for _, p := range postings {
		if (*seen)[p.DocID] {
			continue
		}
		(*seen)[p.DocID] = true
		lines, _ := e.readFileLines(p.DocID)
		if lines == nil {
			continue
		}
		tf := float64(p.Frequency) / float64(len(lines)+1)
		score := 0.85 + 0.15*math.Min(tf, 1.0)

		for _, pos := range p.Positions {
			if pos >= 0 && pos < len(lines) {
				matches = append(matches, Match{
					Path:    p.DocID,
					Line:    pos + 1,
					Content: strings.TrimSpace(lines[pos]),
					Score:   score,
				})
			}
		}
	}
	return matches
}

func (e *Engine) searchTokens(tokens []string, prefixMatch bool, seen *map[string]bool) []Match {
	candidates := make(map[string]*aggregatedMatch)

	for _, token := range tokens {
		postings, ok := e.index[token]
		if !ok {
			continue
		}
		for _, p := range postings {
			key := p.DocID
			if (*seen)[key] {
				continue
			}
			am, exists := candidates[key]
			if !exists {
				am = &aggregatedMatch{
					docID:    key,
					totalTF:  0,
					matchCnt: 0,
				}
				candidates[key] = am
			}
			am.totalTF += float64(p.Frequency)
			am.matchCnt++
		}
	}

	return e.flushCandidates(candidates, tokens, seen, prefixMatch)
}

func (e *Engine) searchPhrase(tokens []string, seen *map[string]bool) []Match {
	candidates := make(map[string]*aggregatedMatch)
	firstToken := tokens[0]
	firstPostings, ok := e.index[firstToken]
	if !ok {
		return nil
	}

	phraseSpace := strings.ToLower(strings.Join(tokens, " "))
	phraseFlat := strings.ReplaceAll(phraseSpace, " ", "")

	for _, fp := range firstPostings {
		if (*seen)[fp.DocID] {
			continue
		}
		if !e.hasPhrase(fp.DocID, phraseSpace, phraseFlat) {
			continue
		}
		if _, exists := candidates[fp.DocID]; !exists {
			candidates[fp.DocID] = &aggregatedMatch{
				docID:    fp.DocID,
				totalTF:  float64(fp.Frequency),
				matchCnt: 1,
			}
		}
	}

	return e.flushCandidates(candidates, tokens, seen, true)
}

func (e *Engine) hasPhrase(docID string, phraseSpace, phraseFlat string) bool {
	if _, ok := e.docs[docID]; !ok {
		return false
	}

	fullPath := filepath.Join(e.root, docID)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false
	}

	content := string(data)
	lower := strings.ToLower(content)
	noSpace := strings.ReplaceAll(lower, " ", "")
	return strings.Contains(lower, phraseSpace) || strings.Contains(noSpace, phraseFlat)
}

func (e *Engine) searchFuzzy(query string, tokens []string, maxDist int, seen *map[string]bool) []Match {
	candidates := make(map[string]*aggregatedMatch)

	for token := range e.index {
		if strings.HasPrefix(token, "_") {
			continue
		}
		for _, qt := range tokens {
			dist := levenshtein(token, qt)
			if dist > maxDist {
				continue
			}
			postings := e.index[token]
			for _, p := range postings {
				if (*seen)[p.DocID] {
					continue
				}
				key := p.DocID
				am, exists := candidates[key]
				if !exists {
					am = &aggregatedMatch{
						docID:    key,
						totalTF:  0,
						matchCnt: 0,
					}
					candidates[key] = am
				}
				similarity := 1.0 - float64(dist)/float64(maxTokenLen(token, qt))
				am.totalTF += float64(p.Frequency) * similarity
				am.matchCnt++
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	var matches []Match
	for _, am := range candidates {
		(*seen)[am.docID] = true
		lines, err := e.readFileLines(am.docID)
		if err != nil || lines == nil {
			continue
		}
		score := 0.5 + 0.3*(am.totalTF/float64(len(lines)+1))
		if score > 1.0 {
			score = 1.0
		}
		for i, line := range lines {
			matches = append(matches, Match{
				Path:    am.docID,
				Line:    i + 1,
				Content: strings.TrimSpace(line),
				Score:   score,
			})
		}
		if len(matches) > 200 {
			break
		}
	}

	return matches
}

type aggregatedMatch struct {
	docID    string
	totalTF  float64
	matchCnt int
}

func (e *Engine) flushCandidates(candidates map[string]*aggregatedMatch, tokens []string, seen *map[string]bool, prefixMatch bool) []Match {
	if len(candidates) == 0 {
		return nil
	}

	var matches []Match
	for _, am := range candidates {
		(*seen)[am.docID] = true
		lines, _ := e.readFileLines(am.docID)
		if lines == nil {
			continue
		}
		score := 0.5 + 0.3*(am.totalTF/float64(len(lines)+1))
		if prefixMatch && am.matchCnt == len(tokens) {
			score = math.Min(score+0.2, 1.0)
		}
		if score > 1.0 {
			score = 1.0
		}

		for i, line := range lines {
			if containsToken(line, tokens) {
				matches = append(matches, Match{
					Path:    am.docID,
					Line:    i + 1,
					Content: strings.TrimSpace(line),
					Score:   score,
				})
			}
		}

		if len(matches) > 200 {
			break
		}
	}

	return matches
}

func (e *Engine) removeDoc(path string) {
	for token, pl := range e.index {
		filtered := pl[:0]
		for _, p := range pl {
			if p.DocID != path {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			delete(e.index, token)
		} else {
			e.index[token] = filtered
		}
	}
	delete(e.docs, path)
}

func (e *Engine) addDoc(path string, doc *Document, content string) {
	e.docs[path] = doc
	tokens := tokenize(content)
	positions := make(map[string][]int)

	for i, tok := range tokens {
		positions[tok] = append(positions[tok], i)
	}

	for tok, pos := range positions {
		entry := Posting{
			DocID:     path,
			Frequency: len(pos),
			Positions: pos,
		}
		e.index[tok] = append(e.index[tok], entry)
	}
}

func (e *Engine) readFileLines(path string) ([]string, error) {
	fullPath := filepath.Join(e.root, path)
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func tokenize(text string) []string {
	var tokens []string
	var current []rune

	flush := func() {
		if len(current) < 2 {
			return
		}
		tokens = append(tokens, strings.ToLower(string(current)))
	}

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current = append(current, r)
		} else {
			flush()
			current = current[:0]
		}
	}
	flush()

	return tokens
}

func containsToken(line string, tokens []string) bool {
	lower := strings.ToLower(line)
	for _, tok := range tokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

func shouldIndex(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".rs", ".c", ".h",
		".cpp", ".hpp", ".cc", ".hh", ".cs", ".rb", ".swift", ".kt", ".kts",
		".scala", ".php", ".pl", ".pm", ".sh", ".bash", ".zsh", ".fish",
		".yaml", ".yml", ".toml", ".json", ".xml", ".html", ".css", ".scss",
		".less", ".sql", ".md", ".txt", ".env", ".dockerfile", ".makefile":
		return true
	}
	return strings.EqualFold(filepath.Base(path), "dockerfile") ||
		strings.EqualFold(filepath.Base(path), "makefile") ||
		strings.EqualFold(filepath.Base(path), "gemfile")
}

func detectLang(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".hpp", ".cc", ".hh":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".rb":
		return "ruby"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	case ".sql":
		return "sql"
	case ".sh", ".bash", ".zsh", ".fish":
		return "shell"
	default:
		return "text"
	}
}

func levenshtein(s, t string) int {
	if len(s) == 0 {
		return len(t)
	}
	if len(t) == 0 {
		return len(s)
	}

	d := make([][]int, len(s)+1)
	for i := range d {
		d[i] = make([]int, len(t)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}

	for i := 1; i <= len(s); i++ {
		for j := 1; j <= len(t); j++ {
			cost := 1
			if s[i-1] == t[j-1] {
				cost = 0
			}
			d[i][j] = min3(
				d[i-1][j]+1,
				d[i][j-1]+1,
				d[i-1][j-1]+cost,
			)
		}
	}

	return d[len(s)][len(t)]
}

func maxTokenLen(a, b string) int {
	if len(a) > len(b) {
		return len(a)
	}
	return len(b)
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

type Stats struct {
	DocCount   int
	TokenCount int
	IndexSize  int64
}

func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Stats{
		DocCount:   len(e.docs),
		TokenCount: len(e.index),
	}
}
