package command

import (
	"strings"
	"sync"
	"testing"
)

func TestPermissionSetHas(t *testing.T) {
	set := PermissionSet(PermRead | PermWrite)
	if !set.Has(PermRead) {
		t.Error("expected set to have PermRead")
	}
	if !set.Has(PermWrite) {
		t.Error("expected set to have PermWrite")
	}
	if set.Has(PermExecute) {
		t.Error("did not expect set to have PermExecute")
	}
	if set.Has(PermAnalyze) {
		t.Error("did not expect set to have PermAnalyze")
	}
}

func TestPermissionSetContains(t *testing.T) {
	tests := []struct {
		name     string
		set      PermissionSet
		required PermissionSet
		want     bool
	}{
		{"empty required is contained", PermissionSet(PermRead), 0, true},
		{"exact match", PermissionSet(PermRead | PermAnalyze), PermissionSet(PermRead | PermAnalyze), true},
		{"subset", PermissionSet(PermRead | PermAnalyze | PermWrite), PermissionSet(PermRead | PermAnalyze), true},
		{"missing bit", PermissionSet(PermRead), PermissionSet(PermRead | PermAnalyze), false},
		{"disjoint", PermissionSet(PermWrite), PermissionSet(PermRead), false},
	}
	for _, tc := range tests {
		if got := tc.set.Contains(tc.required); got != tc.want {
			t.Errorf("%s: Contains(%s) = %v, want %v", tc.name, tc.required, got, tc.want)
		}
	}
}

func TestWorkspacePermissions(t *testing.T) {
	tests := []struct {
		ws    WorkspaceType
		want  PermissionSet
		has   Permission
		lacks Permission
	}{
		{WorkspaceAsk, PermissionSet(PermRead), PermRead, PermWrite},
		{WorkspaceInvestigate, PermissionSet(PermRead | PermAnalyze), PermAnalyze, PermExecute},
		{WorkspacePlan, PermissionSet(PermRead | PermAnalyze), PermRead, PermExecute},
		{WorkspaceBuild, PermissionSet(PermRead | PermAnalyze | PermWrite | PermExecute), PermWrite, 0},
		{WorkspaceReview, PermissionSet(PermRead | PermAnalyze | PermExecute), PermExecute, PermWrite},
	}
	for _, tc := range tests {
		got := tc.ws.Permissions()
		if got != tc.want {
			t.Errorf("%s: Permissions() = %s, want %s", tc.ws, got, tc.want)
		}
		if !got.Has(tc.has) {
			t.Errorf("%s: expected Permissions() to have %s", tc.ws, tc.has)
		}
		if tc.lacks != 0 && got.Has(tc.lacks) {
			t.Errorf("%s: did not expect Permissions() to have %s", tc.ws, tc.lacks)
		}
	}
}

// TestGetAllowedDirectivesAskIsReadOnly verifies the primary DoD: WorkspaceAsk
// must return ONLY read-based directives — nothing that requires analyze,
// execute, or write.
func TestGetAllowedDirectivesAskIsReadOnly(t *testing.T) {
	ds := Default().GetAllowedDirectives(WorkspaceAsk)
	if len(ds) == 0 {
		t.Fatal("expected WorkspaceAsk to allow at least one read-based directive")
	}
	for _, d := range ds {
		if d.Marker != MarkerDollar {
			t.Errorf("GetAllowedDirectives returned non-directive %q (marker %q)", d.Name, d.Marker)
		}
		if !d.RequiredPerms.Has(PermRead) {
			t.Errorf("directive %q is not read-based (RequiredPerms=%s)", d.Name, d.RequiredPerms)
		}
		for _, p := range []Permission{PermAnalyze, PermExecute, PermWrite} {
			if d.RequiredPerms.Has(p) {
				t.Errorf("directive %q requires %s, which WorkspaceAsk must not grant", d.Name, p)
			}
		}
	}
}

// TestGetAllowedDirectivesAskExact verifies the exact read-only directive set
// exposed by WorkspaceAsk.
func TestGetAllowedDirectivesAskExact(t *testing.T) {
	got := names(Default().GetAllowedDirectives(WorkspaceAsk))
	want := []string{"env", "prompt"}
	if !equalStrings(got, want) {
		t.Errorf("WorkspaceAsk directives = %v, want %v", got, want)
	}
}

// TestGetAllowedDirectivesBuildReturnsAll verifies the second DoD: WorkspaceBuild
// must return every registered directive.
func TestGetAllowedDirectivesBuildReturnsAll(t *testing.T) {
	got := names(Default().GetAllowedDirectives(WorkspaceBuild))
	want := []string{"diagnose", "env", "fix", "hot", "prompt", "run", "test", "trace"}
	if !equalStrings(got, want) {
		t.Errorf("WorkspaceBuild directives = %v, want %v", got, want)
	}
	if len(got) != len(want) {
		t.Errorf("WorkspaceBuild returned %d directives, want all %d", len(got), len(want))
	}
}

// TestGetAllowedDirectivesSubsetLogic verifies the bitwise subset behavior for
// the intermediate workspaces: investigate/plan never receive execute or write
// directives, review receives execute (run/test) but never write (hot/fix).
func TestGetAllowedDirectivesSubsetLogic(t *testing.T) {
	tests := []struct {
		ws        WorkspaceType
		forbidden []string
	}{
		{WorkspaceInvestigate, []string{"hot", "fix", "run", "test"}},
		{WorkspacePlan, []string{"hot", "fix", "run", "test"}},
		{WorkspaceReview, []string{"hot", "fix"}},
	}
	for _, tc := range tests {
		allowed := map[string]bool{}
		for _, d := range Default().GetAllowedDirectives(tc.ws) {
			allowed[d.Name] = true
		}
		for _, f := range tc.forbidden {
			if allowed[f] {
				t.Errorf("%s must not allow directive %q", tc.ws, f)
			}
		}
	}
}

func TestLookup(t *testing.T) {
	r := NewDefault()
	tests := []struct {
		marker rune
		name   string
		ok     bool
	}{
		{MarkerSlash, "build", true},
		{MarkerSlash, "BUILD", true}, // case-insensitive
		{MarkerSlash, "help", true},
		{MarkerDollar, "hot", true},
		{MarkerDollar, "test", true},
		{MarkerSlash, "hot", false}, // wrong marker family
		{MarkerDollar, "build", false},
		{MarkerSlash, "bogus", false},
		{MarkerAt, "internal/auth.go", false}, // scopes are dynamic, never registered
	}
	for _, tc := range tests {
		d, ok := r.Lookup(tc.marker, tc.name)
		if ok != tc.ok {
			t.Errorf("Lookup(%q, %q) ok = %v, want %v", tc.marker, tc.name, ok, tc.ok)
			continue
		}
		if ok {
			if d.Marker != tc.marker || d.Name != strings.ToLower(tc.name) {
				t.Errorf("Lookup(%q, %q) returned %q (marker %q), want name %q", tc.marker, tc.name, d.Name, d.Marker, tc.name)
			}
		}
	}
}

func TestLookupReturnsCopy(t *testing.T) {
	r := NewDefault()
	d, ok := r.Lookup(MarkerSlash, "build")
	if !ok {
		t.Fatal("expected build to exist")
	}
	d.Name = "mutated"
	again, ok := r.Lookup(MarkerSlash, "build")
	if !ok {
		t.Fatal("expected build to still exist")
	}
	if again.Name != "build" {
		t.Errorf("Lookup returned a mutable alias; got %q", again.Name)
	}
}

// TestRegistryConcurrent exercises the thread-safety contract under the race
// detector.
func TestRegistryConcurrent(t *testing.T) {
	r := NewDefault()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Lookup(MarkerDollar, "hot")
				r.GetAllowedDirectives(WorkspaceBuild)
			}
		}()
	}
	wg.Wait()
}

func TestDefaultSingleton(t *testing.T) {
	first := Default()
	second := Default()
	if first == nil {
		t.Fatal("Default() returned nil")
	}
	if first != second {
		t.Error("Default() must return the same singleton instance")
	}
}

// names returns the marker-prefixed names of the descriptors in d.
func names(ds []CommandDescriptor) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
