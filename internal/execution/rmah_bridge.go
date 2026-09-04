package execution

import (
	"strings"

	"github.com/PizenLabs/izen/internal/execution/rmah"
)

// rmahMaxCandidateBytes bounds the largest candidate the RMAH Tier 2
// extractor will consider. It mirrors MaxFullContentRewriteBytes so a
// free-tier model cannot flood the safety barriers with a multi-megabyte
// payload.
const rmahMaxCandidateBytes = 50 * 1024

// rmahPipeline is the package-level RMAH pipeline instance used by the
// RuntimeExecutor as a fallback when Tier 1 (strict schema parsing) fails.
// It is wired into invokeMutation so free-tier models that return raw code
// fences instead of SEARCH/REPLACE blocks can still produce valid mutation
// candidates — without bypassing AST baseline checks or hard-block bounds.
var rmahPipeline = rmah.NewConfiguredPipeline(
	rmahMaxCandidateBytes,
	tier1StrictSchema,
	tier2CodeExtract,
	tier2BaselineVerify,
)

// tier1StrictSchema is the Tier 1 function: validates rawLLMOutput against
// strict SEARCH/REPLACE blocks or unified diff contracts. It reuses the
// existing ExtractBoundedPatch so the bounded-patch contract and RMAH Tier 1
// never diverge.
func tier1StrictSchema(rawLLMOutput, original string) (string, bool) {
	candidate, ok := ExtractBoundedPatch(original, rawLLMOutput)
	if !ok || candidate == "" {
		return "", false
	}
	return candidate, true
}

// tier2CodeExtract is the Tier 2 function: extracts raw code content from
// fenced blocks in the LLM output. It reuses the existing
// ExtractCodeBlockContent for consistency with the rest of the codebase.
func tier2CodeExtract(rawLLMOutput string) (string, bool) {
	return ExtractCodeBlockContent(rawLLMOutput)
}

// tier2BaselineVerify reports whether content for the given target file has a
// valid, clean parseable structure. It is the AST baseline check used by
// RMAH Tier 2 to detect degradation. For registered languages (Go, HTML,
// JSON) it delegates to the shared V3 pipeline; for unregistered languages it
// returns true (no baseline to degrade — conservative default).
func tier2BaselineVerify(content, target string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	tag := ValidatorTagForPath(target)
	if tag == "" {
		return true
	}
	gate := ValidateContentForPath(target, []byte(content), 0)
	return gate.Passed
}
