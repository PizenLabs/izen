package capability

// Profile represents a named capability profile.
type Profile int

const (
	ProfileInformationRetrieval Profile = iota
	ProfileDiagnosticsInvestigation
	ProfileCodeMutation
)

// String returns the human-readable profile name.
func (p Profile) String() string {
	switch p {
	case ProfileInformationRetrieval:
		return "information-retrieval"
	case ProfileDiagnosticsInvestigation:
		return "diagnostics-investigation"
	case ProfileCodeMutation:
		return "code-mutation"
	default:
		return "unknown"
	}
}

// ProfileFor returns a CapabilitySet configured for the given profile.
func ProfileFor(p Profile) *CapabilitySet {
	switch p {
	case ProfileInformationRetrieval:
		return informationRetrievalProfile()
	case ProfileDiagnosticsInvestigation:
		return diagnosticsInvestigationProfile()
	case ProfileCodeMutation:
		return codeMutationProfile()
	default:
		return informationRetrievalProfile()
	}
}

// informationRetrievalProfile grants read-only access. Write, test, patch,
// and execute are denied.
func informationRetrievalProfile() *CapabilitySet {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityRead)
	return cs
}

// diagnosticsInvestigationProfile grants read + test. Execute is restricted
// to diagnostic/read-only commands. Write and patch are denied.
func diagnosticsInvestigationProfile() *CapabilitySet {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityRead)
	cs.Grant(CapabilityTest)
	cs.Grant(CapabilityExecute,
		ScopeRule{
			Capability: CapabilityExecute,
			Patterns: []string{
				"go test",
				"go vet",
				"go build",
				"go mod",
				"go list",
				"golangci-lint",
				"staticcheck",
				"git log",
				"git diff",
				"git show",
				"git status",
				"which",
				"ls",
				"cat",
				"head",
				"tail",
				"echo",
				"printf",
			},
		},
	)
	return cs
}

// codeMutationProfile grants read, write, test, and patch. Execute is
// restricted to test/verify commands. Checkpoint is required.
func codeMutationProfile() *CapabilitySet {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityRead)
	cs.Grant(CapabilityWrite)
	cs.Grant(CapabilityTest)
	cs.Grant(CapabilityPatch)
	cs.Grant(CapabilityCheckpoint)
	cs.Grant(CapabilityExecute,
		ScopeRule{
			Capability: CapabilityExecute,
			Patterns: []string{
				"go test",
				"go build",
				"go vet",
				"go mod",
				"go fmt",
				"golangci-lint",
				"staticcheck",
			},
		},
	)
	return cs
}
