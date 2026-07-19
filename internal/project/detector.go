package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/language"
)

type Detection struct {
	Primary      *language.Def       `json:"primary"`
	Secondary    []language.Detected `json:"secondary,omitempty"`
	Frameworks   []language.Detected `json:"frameworks,omitempty"`
	BuildSystems []language.Detected `json:"build_systems,omitempty"`
	Confidence   float64             `json:"confidence"`
}

type fileEvidence struct {
	def     *language.Def
	matched string
	score   float64
}

func Detect(root string) Detection {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Detection{}
	}

	reg := language.Global()
	var evidence []fileEvidence
	matched := make(map[string]bool)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".izen" {
				continue
			}
		}

		if def, ok := reg.FromIndicatorFile(name); ok {
			score := indicatorScore(name)
			evidence = append(evidence, fileEvidence{def: def, matched: name, score: score})
			matched[strings.ToLower(name)] = true
		}

		ext := strings.ToLower(filepath.Ext(name))
		if ext != "" && !matched[strings.ToLower(name)] {
			if def, ok := reg.FromExtension(ext); ok {
				evidence = append(evidence, fileEvidence{def: def, matched: name, score: 0.3})
			}
		}
	}

	return classify(evidence, reg)
}

func indicatorScore(name string) float64 {
	high := map[string]float64{
		"go.mod": 1.0, "Cargo.toml": 1.0, "package.json": 0.9,
		"pom.xml": 0.9, "build.gradle": 0.9, "build.gradle.kts": 0.9,
		"Gemfile": 0.9, "composer.json": 0.9, "setup.py": 0.8,
		"pyproject.toml": 0.8, "requirements.txt": 0.7,
		"CMakeLists.txt": 0.8, "Makefile": 0.6,
		"go.sum": 0.6, "Cargo.lock": 0.6, "package-lock.json": 0.5,
	}
	if s, ok := high[name]; ok {
		return s
	}
	for _, glob := range []string{"*.csproj", "*.sln"} {
		if matched, _ := filepath.Match(glob, name); matched {
			return 0.9
		}
	}
	return 0.4
}

func classify(evidence []fileEvidence, reg *language.Registry) Detection {
	if len(evidence) == 0 {
		return Detection{}
	}

	scores := make(map[language.ID]float64)

	for _, ev := range evidence {
		if ev.def != nil {
			scores[ev.def.ID] += ev.score
		}
	}

	type scoredLang struct {
		id    language.ID
		score float64
	}
	var scored []scoredLang
	for id, s := range scores {
		scored = append(scored, scoredLang{id: id, score: s})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	if len(scored) == 0 {
		return Detection{}
	}

	primary := scored[0]
	total := 0.0
	for _, s := range scored {
		total += s.score
	}

	def, _ := reg.Lookup(primary.id)
	det := Detection{
		Confidence: primary.score / total,
	}

	if def != nil {
		det.Primary = def
	}

	for i := 1; i < len(scored); i++ {
		if d, ok := reg.Lookup(scored[i].id); ok {
			det.Secondary = append(det.Secondary, language.Detected{
				Def:    d,
				Weight: scored[i].score / total,
			})
		}
	}

	return det
}
