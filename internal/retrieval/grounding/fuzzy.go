package grounding

import (
	"strings"
	"unicode"
)

func Levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	col := make([]int, lb+1)
	for i := 1; i <= lb; i++ {
		col[i] = i
	}
	for i := 1; i <= la; i++ {
		col[0] = i
		prev := i - 1
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			val := min(col[j]+1, col[j-1]+1, prev+cost)
			prev = col[j]
			col[j] = val
		}
	}
	return col[lb]
}

func min(a, b, c int) int {
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

type Match struct {
	Keyword string
	Score   float64
}

type FuzzyMatcher struct {
	dictionary map[string][]string
	threshold  float64
}

func NewFuzzyMatcher(threshold float64) *FuzzyMatcher {
	return &FuzzyMatcher{
		dictionary: DefaultDictionary(),
		threshold:  threshold,
	}
}

func NewFuzzyMatcherWithDict(dict map[string][]string, threshold float64) *FuzzyMatcher {
	return &FuzzyMatcher{
		dictionary: dict,
		threshold:  threshold,
	}
}

func (fm *FuzzyMatcher) Match(word string) *Match {
	word = strings.TrimSpace(strings.ToLower(word))
	if word == "" || len([]rune(word)) < 2 {
		return nil
	}

	var best *Match
	for keyword, variants := range fm.dictionary {
		lowerKW := strings.ToLower(keyword)
		if word == lowerKW {
			return &Match{Keyword: keyword, Score: 1.0}
		}
		for _, v := range variants {
			if word == strings.ToLower(v) {
				return &Match{Keyword: keyword, Score: 1.0}
			}
		}

		for _, v := range append(variants, keyword) {
			dist := Levenshtein(word, strings.ToLower(v))
			maxLen := max(len([]rune(word)), len([]rune(v)))
			if maxLen == 0 {
				continue
			}
			score := 1.0 - float64(dist)/float64(maxLen)
			if score >= fm.threshold && (best == nil || score > best.Score) {
				best = &Match{Keyword: keyword, Score: score}
			}
		}
	}
	return best
}

func (fm *FuzzyMatcher) BestMatch(words []string) *Match {
	var best *Match
	for _, w := range words {
		if m := fm.Match(w); m != nil && (best == nil || m.Score > best.Score) {
			best = m
		}
	}
	return best
}

func DefaultDictionary() map[string][]string {
	return map[string][]string{
		"navigation":     {"navi", "nav", "naviagation", "navigat", "navingation"},
		"button":         {"btn", "buton", "buttn", "buton", "buttin"},
		"header":         {"heder", "headr", "hdr", "headrr"},
		"style":          {"styl", "stle", "css", "sty"},
		"layout":         {"layot", "layut", "lo", "lay"},
		"authentication": {"auth", "authen", "autentication", "authn"},
		"footer":         {"foter", "footr", "ftr", "foorer"},
		"sidebar":        {"sideb", "sbar", "side-bar", "sb"},
		"content":        {"contnt", "cntnt", "conent"},
		"form":           {"frm", "from"},
		"input":          {"inpt", "inp"},
		"modal":          {"modl", "mdal", "mod"},
		"dropdown":       {"dropdwn", "ddown", "drop"},
		"menu":           {"men", "mnu", "menue"},
		"search":         {"serch", "srch", "searc"},
		"login":          {"log", "lgn", "loggin"},
		"register":       {"reg", "rgstr", "signup", "rgster"},
		"color":          {"colour", "clr", "colr"},
		"font":           {"fnt", "fonts"},
		"animation":      {"anim", "anime", "animat"},
		"responsive":     {"resp", "rsponsive", "respon"},
		"alignment":      {"align", "algn", "alig"},
		"padding":        {"pad", "padd", "pading"},
		"margin":         {"marg", "margn", "mgn"},
		"border":         {"brdr", "bordr", "boder"},
		"background":     {"bg", "backg", "bckgrnd"},
		"image":          {"img", "imge", "pic"},
		"table":          {"tbl", "tabl", "tabe"},
		"link":           {"lnk", "lik"},
		"list":           {"lst", "lis"},
		"grid":           {"grd", "gid"},
		"flex":           {"flx", "fles"},
		"card":           {"crd", "cardd"},
		"avatar":         {"avt", "avtr", "avatar"},
		"badge":          {"bdg", "badg"},
		"toast":          {"tost", "toost"},
		"spinner":        {"spin", "spiner", "spnner"},
		"skeleton":       {"skel", "skele", "skeleton"},
		"progress":       {"prog", "progres", "prgs"},
		"slider":         {"slidr", "slid", "slder"},
		"carousel":       {"caro", "crrousel", "carosel"},
		"breadcrumb":     {"bread", "brdcrumb", "bc"},
		"pagination":     {"pagi", "pagnation", "pg"},
		"tooltip":        {"tip", "tooltip"},
		"popover":        {"pop", "popvr", "popover"},
		"drawer":         {"draw", "drwr"},
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isNoiseWord(s string) bool {
	noise := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "can": true, "shall": true, "to": true,
		"of": true, "in": true, "on": true, "at": true, "by": true,
		"with": true, "from": true, "for": true, "as": true, "into": true,
		"through": true, "during": true, "before": true, "after": true,
		"above": true, "below": true, "between": true, "out": true,
		"off": true, "over": true, "under": true, "again": true,
		"further": true, "then": true, "once": true, "here": true,
		"there": true, "when": true, "where": true, "why": true, "how": true,
		"all": true, "each": true, "every": true, "both": true,
		"few": true, "more": true, "most": true, "other": true, "some": true,
		"such": true, "no": true, "nor": true, "not": true, "only": true,
		"own": true, "same": true, "so": true, "than": true, "too": true,
		"very": true, "just": true, "because": true, "about": true,
		"up": true, "down": true, "also": true, "well": true,
		"it": true, "its": true, "this": true, "that": true, "these": true,
		"those": true, "i": true, "me": true, "my": true, "we": true,
		"our": true, "you": true, "your": true, "he": true, "she": true,
		"him": true, "her": true, "they": true, "them": true, "their": true,
		"what": true, "which": true, "who": true, "whom": true,
		"&": true, "and": true, "or": true, "but": true, "if": true,
		"please": true, "make": true, "want": true, "need": true,
		"get": true, "set": true, "put": true, "let": true, "use": true,
		"like": true, "take": true,
	}
	return noise[s]
}

func Tokenize(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var tokens []string
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
		} else {
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		tokens = append(tokens, buf.String())
	}
	return tokens
}

func Words(s string) []string {
	tokens := Tokenize(s)
	if len(tokens) == 0 {
		return nil
	}
	result := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		result = append(result, t)
	}
	return result
}
