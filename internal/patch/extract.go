package patch

import (
	"strings"
)

// CodeFile is one file block extracted from a multi-file LLM response.
type CodeFile struct {
	// Path is the resolved target path from the fence or FILE header.
	Path string
	// Lang is the code fence language tag (empty for === FILE: blocks).
	Lang string
	// Content is the raw block body, trailing newline included.
	Content string
	// Source records the block form that produced the file: "fence" for a
	// markdown code fence (```lang:path etc.) and "file-block" for the
	// deterministic === FILE: protocol.
	Source string
}

// Block sources reported on CodeFile.Source.
const (
	// SourceFence marks a block extracted from a markdown code fence.
	SourceFence = "fence"
	// SourceFileBlock marks a block extracted from the === FILE: protocol.
	SourceFileBlock = "file-block"
)

// ParseFileHeader splits the text after the opening ``` fence into a language
// tag and a target path. Every standard LLM header variation is understood:
//
//	"html:index.html"   -> lang "html",  path "index.html"
//	"js script.js"      -> lang "js",    path "script.js"
//	"file=index.html"   -> lang "",      path "index.html"
//	"html"              -> lang "html",  path ""
//
// ok=false when no path can be resolved (an empty header or a bare language
// tag): the block's target file is unknowable without the model saying so.
func ParseFileHeader(header string) (lang, path string, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", "", false
	}
	if lower := strings.ToLower(header); strings.HasPrefix(lower, "file=") {
		path = strings.TrimSpace(header[len("file="):])
		return "", path, path != ""
	}
	if idx := strings.IndexByte(header, ':'); idx >= 0 {
		lang = strings.TrimSpace(header[:idx])
		path = strings.TrimSpace(header[idx+1:])
		return lang, path, path != ""
	}
	if fields := strings.Fields(header); len(fields) >= 2 {
		return fields[0], fields[1], true
	}
	return strings.TrimSpace(header), "", false
}

// ParseCodeFences extracts multi-file blocks from an LLM response. It accepts
// every standard path-header variation — markdown code fences carrying an
// inline target (```lang:path, ```lang path, ```file=path) and the
// deterministic === FILE: ... === END protocol. Blocks are returned in order of
// appearance; fence blocks whose opening line carries no path (a bare ```lang)
// are skipped because their target file is unknowable.
func ParseCodeFences(text string) []CodeFile {
	return parseFences(text, true)
}

// ParseMarkdownFences is ParseCodeFences restricted to markdown code fences.
// The === FILE: protocol is left untouched so the callers that own it (e.g.
// the layer3 worker) never see duplicated blocks.
func ParseMarkdownFences(text string) []CodeFile {
	return parseFences(text, false)
}

// parseFences implements ParseCodeFences / ParseMarkdownFences. When
// includeFileProtocol is true the === FILE: ... === END protocol is recognized
// alongside markdown code fences. The two block forms close on their own
// terminators only, so a === FILE: block may contain a markdown fence and a
// fence block may contain === END without being cut short.
func parseFences(text string, includeFileProtocol bool) []CodeFile {
	var out []CodeFile
	var cur *CodeFile
	inFence := false
	inFile := false

	flush := func() {
		if cur != nil {
			out = append(out, *cur)
		}
		cur = nil
		inFence = false
		inFile = false
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inFence:
			if strings.HasPrefix(trimmed, "```") {
				flush()
				continue
			}
			cur.Content += line + "\n"
		case inFile:
			if trimmed == "=== END" {
				flush()
				continue
			}
			if strings.HasPrefix(trimmed, "=== FILE:") {
				flush()
				path := strings.TrimSpace(strings.TrimPrefix(trimmed, "=== FILE:"))
				if path != "" {
					cur = &CodeFile{Path: path, Source: SourceFileBlock}
					inFile = true
				}
				continue
			}
			cur.Content += line + "\n"
		default:
			if strings.HasPrefix(trimmed, "```") {
				header := strings.TrimPrefix(trimmed, "```")
				if lang, path, ok := ParseFileHeader(header); ok {
					cur = &CodeFile{Path: path, Lang: lang, Source: SourceFence}
					inFence = true
				}
				continue
			}
			if includeFileProtocol && strings.HasPrefix(trimmed, "=== FILE:") {
				path := strings.TrimSpace(strings.TrimPrefix(trimmed, "=== FILE:"))
				if path != "" {
					cur = &CodeFile{Path: path, Source: SourceFileBlock}
					inFile = true
				}
				continue
			}
		}
	}
	flush()
	return out
}

// FullFilePatch converts an extracted code file into a full-rewrite Patch
// (Tier3WholeFile) representing complete file creation or overwrite. The
// Modified content is the block body with its trailing newline trimmed, and
// Original is left empty — the caller snapshots the on-disk content when the
// file already exists. No diff markers are ever required for a new file.
func (f CodeFile) FullFilePatch() Patch {
	return Patch{
		File:     f.Path,
		Modified: strings.TrimSuffix(f.Content, "\n"),
		Tier:     Tier3WholeFile,
		Strategy: Tier3WholeFile.String(),
	}
}
