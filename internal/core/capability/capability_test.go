package capability

import (
	"testing"
)

func TestCapability_String(t *testing.T) {
	tests := []struct {
		c    Capability
		want string
	}{
		{CapabilityRead, "read"},
		{CapabilityWrite, "write"},
		{CapabilityExecute, "execute"},
		{CapabilityTest, "test"},
		{CapabilityPatch, "patch"},
		{CapabilityCheckpoint, "checkpoint"},
		{CapabilityRollback, "rollback"},
		{Capability(1 << 7), "capability(128)"},
	}
	for _, tc := range tests {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("Capability(%d).String() = %q, want %q", int(tc.c), got, tc.want)
		}
	}
}

func TestCapabilitySet_GrantAndHas(t *testing.T) {
	cs := NewCapabilitySet()
	if cs.Has(CapabilityRead) {
		t.Error("Has(Read) = true before grant")
	}
	cs.Grant(CapabilityRead)
	if !cs.Has(CapabilityRead) {
		t.Error("Has(Read) = false after grant")
	}
	if cs.Has(CapabilityWrite) {
		t.Error("Has(Write) = true when only Read was granted")
	}
}

func TestCapabilitySet_Deny(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityRead)
	cs.Grant(CapabilityWrite)
	cs.Deny(CapabilityRead)
	if cs.Has(CapabilityRead) {
		t.Error("Has(Read) = true after Deny")
	}
	if !cs.Has(CapabilityWrite) {
		t.Error("Has(Write) = false after denying Read")
	}
}

func TestCapabilitySet_CanReadWrite(t *testing.T) {
	cs := NewCapabilitySet()
	if cs.CanRead() {
		t.Error("CanRead() = true before grant")
	}
	cs.Grant(CapabilityRead)
	if !cs.CanRead() {
		t.Error("CanRead() = false after grant")
	}
	if cs.CanWrite() {
		t.Error("CanWrite() = true when only Read was granted")
	}
	cs.Grant(CapabilityWrite)
	if !cs.CanWrite() {
		t.Error("CanWrite() = false after grant")
	}
}

func TestCapabilitySet_CanTestPatchCheckpointRollback(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityTest)
	cs.Grant(CapabilityPatch)
	cs.Grant(CapabilityCheckpoint)
	cs.Grant(CapabilityRollback)

	if !cs.CanTest() {
		t.Error("CanTest() = false after grant")
	}
	if !cs.CanPatch() {
		t.Error("CanPatch() = false after grant")
	}
	if !cs.CanCheckpoint() {
		t.Error("CanCheckpoint() = false after grant")
	}
	if !cs.CanRollback() {
		t.Error("CanRollback() = false after grant")
	}
}

func TestCapabilitySet_GrantFromSet(t *testing.T) {
	src := NewCapabilitySet()
	src.Grant(CapabilityRead)
	src.Grant(CapabilityWrite, ScopeRule{Capability: CapabilityWrite, Patterns: []string{"*.go"}})

	dst := NewCapabilitySet()
	dst.GrantFromSet(src)

	if !dst.Has(CapabilityRead) {
		t.Error("dst.Has(Read) = false after GrantFromSet")
	}
	if !dst.Has(CapabilityWrite) {
		t.Error("dst.Has(Write) = false after GrantFromSet")
	}
}

func TestScopeRule_MatchFile_NoPatterns(t *testing.T) {
	r := ScopeRule{Capability: CapabilityWrite}
	if !r.MatchFile("anything.go") {
		t.Error("MatchFile with no patterns should always match")
	}
}

func TestScopeRule_MatchFile_Glob(t *testing.T) {
	r := ScopeRule{Capability: CapabilityWrite, Patterns: []string{"*.go"}}
	if !r.MatchFile("main.go") {
		t.Error("MatchFile(main.go) should match *.go")
	}
	if r.MatchFile("main.rs") {
		t.Error("MatchFile(main.rs) should not match *.go")
	}
}

func TestScopeRule_MatchFile_Substring(t *testing.T) {
	r := ScopeRule{Capability: CapabilityWrite, Patterns: []string{"internal/"}}
	if !r.MatchFile("internal/core/runtime.go") {
		t.Error("MatchFile(internal/core/runtime.go) should match internal/")
	}
	if !r.MatchFile("internal/capability.go") {
		t.Error("MatchFile(internal/capability.go) should match internal/")
	}
}

func TestScopeRule_MatchFile_MultiplePatterns(t *testing.T) {
	r := ScopeRule{Capability: CapabilityWrite, Patterns: []string{"*.go", "*.md"}}
	if !r.MatchFile("readme.md") {
		t.Error("MatchFile(readme.md) should match *.md")
	}
	if r.MatchFile("config.json") {
		t.Error("MatchFile(config.json) should match neither *.go nor *.md")
	}
}

func TestScopeRule_MatchCommand_NoPatterns(t *testing.T) {
	r := ScopeRule{Capability: CapabilityExecute}
	if !r.MatchCommand("anything") {
		t.Error("MatchCommand with no patterns should always match")
	}
}

func TestScopeRule_MatchCommand_Prefix(t *testing.T) {
	r := ScopeRule{Capability: CapabilityExecute, Patterns: []string{"go test", "go build"}}
	if !r.MatchCommand("go test ./...") {
		t.Error("MatchCommand(go test ./...) should match")
	}
	if !r.MatchCommand("go build -o /dev/null") {
		t.Error("MatchCommand(go build -o /dev/null) should match")
	}
	if r.MatchCommand("rm -rf /") {
		t.Error("MatchCommand(rm -rf /) should not match")
	}
}

func TestScopeRule_MatchCommand_CaseInsensitive(t *testing.T) {
	r := ScopeRule{Capability: CapabilityExecute, Patterns: []string{"Go Test"}}
	if !r.MatchCommand("go test ./...") {
		t.Error("MatchCommand should be case-insensitive")
	}
}

func TestCapabilitySet_CanMutateFile_Unscoped(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityWrite)
	if !cs.CanMutateFile("anything.txt") {
		t.Error("CanMutateFile should match any file when Write is granted globally")
	}
}

func TestCapabilitySet_CanMutateFile_Scoped(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityWrite, ScopeRule{Capability: CapabilityWrite, Patterns: []string{"*.go"}})
	if !cs.CanMutateFile("main.go") {
		t.Error("CanMutateFile(main.go) should match *.go scope")
	}
	if cs.CanMutateFile("main.rs") {
		t.Error("CanMutateFile(main.rs) should not match *.go scope")
	}
}

func TestCapabilitySet_CanMutateFile_PatchCapability(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityPatch, ScopeRule{Capability: CapabilityPatch, Patterns: []string{"internal/"}})
	if !cs.CanMutateFile("internal/core/runtime.go") {
		t.Error("CanMutateFile should match via CapabilityPatch scope")
	}
	if cs.CanMutateFile("cmd/main.go") {
		t.Error("CanMutateFile for cmd/ should not match internal/ scope")
	}
}

func TestCapabilitySet_CanExecuteCommand_Unscoped(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityExecute)
	if !cs.CanExecuteCommand("anything") {
		t.Error("CanExecuteCommand should match any command when granted globally")
	}
}

func TestCapabilitySet_CanExecuteCommand_Scoped(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityExecute, ScopeRule{Capability: CapabilityExecute, Patterns: []string{"go test"}})
	if !cs.CanExecuteCommand("go test ./...") {
		t.Error("CanExecuteCommand(go test ./...) should match")
	}
	if cs.CanExecuteCommand("rm -rf") {
		t.Error("CanExecuteCommand(rm -rf) should not match")
	}
}

func TestCapabilitySet_CanExecuteCommand_NotGranted(t *testing.T) {
	cs := NewCapabilitySet()
	cs.Grant(CapabilityRead)
	if cs.CanExecuteCommand("anything") {
		t.Error("CanExecuteCommand should be false when CapabilityExecute not granted")
	}
}

func TestProfile_InformationRetrieval(t *testing.T) {
	cs := ProfileFor(ProfileInformationRetrieval)
	if !cs.CanRead() {
		t.Error("InformationRetrieval: should have Read")
	}
	if cs.CanWrite() {
		t.Error("InformationRetrieval: should deny Write")
	}
	if cs.CanTest() {
		t.Error("InformationRetrieval: should deny Test")
	}
	if cs.CanPatch() {
		t.Error("InformationRetrieval: should deny Patch")
	}
	if cs.CanExecuteCommand("anything") {
		t.Error("InformationRetrieval: should deny Execute")
	}
}

func TestProfile_DiagnosticsInvestigation(t *testing.T) {
	cs := ProfileFor(ProfileDiagnosticsInvestigation)
	if !cs.CanRead() {
		t.Error("DiagnosticsInvestigation: should have Read")
	}
	if !cs.CanTest() {
		t.Error("DiagnosticsInvestigation: should have Test")
	}
	if cs.CanWrite() {
		t.Error("DiagnosticsInvestigation: should deny Write")
	}
	if cs.CanPatch() {
		t.Error("DiagnosticsInvestigation: should deny Patch")
	}
	if !cs.CanExecuteCommand("go test ./...") {
		t.Error("DiagnosticsInvestigation: should allow go test")
	}
	if cs.CanExecuteCommand("rm -rf /") {
		t.Error("DiagnosticsInvestigation: should deny dangerous commands")
	}
}

func TestProfile_CodeMutation(t *testing.T) {
	cs := ProfileFor(ProfileCodeMutation)
	if !cs.CanRead() {
		t.Error("CodeMutation: should have Read")
	}
	if !cs.CanWrite() {
		t.Error("CodeMutation: should have Write")
	}
	if !cs.CanTest() {
		t.Error("CodeMutation: should have Test")
	}
	if !cs.CanPatch() {
		t.Error("CodeMutation: should have Patch")
	}
	if !cs.CanCheckpoint() {
		t.Error("CodeMutation: should require Checkpoint")
	}
	if !cs.CanExecuteCommand("go test ./...") {
		t.Error("CodeMutation: should allow go test")
	}
	if !cs.CanExecuteCommand("golangci-lint run") {
		t.Error("CodeMutation: should allow golangci-lint")
	}
	if cs.CanExecuteCommand("rm -rf /") {
		t.Error("CodeMutation: should deny dangerous commands")
	}
}

func TestProfile_String(t *testing.T) {
	tests := []struct {
		p    Profile
		want string
	}{
		{ProfileInformationRetrieval, "information-retrieval"},
		{ProfileDiagnosticsInvestigation, "diagnostics-investigation"},
		{ProfileCodeMutation, "code-mutation"},
		{Profile(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("Profile(%d).String() = %q, want %q", int(tc.p), got, tc.want)
		}
	}
}

func TestProfileFor_Unknown(t *testing.T) {
	cs := ProfileFor(Profile(99))
	if cs == nil {
		t.Fatal("ProfileFor(unknown) returned nil")
	}
	if !cs.CanRead() {
		t.Error("ProfileFor(unknown) should default to read-only")
	}
}
