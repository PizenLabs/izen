package autonomy

import "testing"

// TestBuildDecisionSurface_PromptAllowsScopeExpansion asserts the $prompt
// systemic/advisory policy: on missing external refs, $prompt generates
// ProposalExpandScope (alongside ProposalInlineDeps) so the human can widen
// the target boundary.
func TestBuildDecisionSurface_PromptAllowsScopeExpansion(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTValid,
		DependencyStatus: DependenciesUnresolved,
		BudgetStatus:     BudgetWithinLimits,
		Findings:         []string{`target "index.html" references missing local file "app.js"`},
	}
	surface := BuildDecisionSurface(eval, "$prompt")

	if !surface.Has(ProposalExpandScope) {
		t.Fatal("$prompt must offer ProposalExpandScope on missing external refs")
	}
	if !surface.Has(ProposalInlineDeps) {
		t.Fatal("$prompt must offer ProposalInlineDeps on missing external refs")
	}
	if surface.Option(ProposalExpandScope) == nil {
		t.Fatal("expand_scope option must be present and addressable by intent")
	}
	if surface.ExternalRefsCount != 1 {
		t.Fatalf("external refs count = %d, want 1", surface.ExternalRefsCount)
	}
	if surface.Target != "index.html" {
		t.Fatalf("target = %q, want index.html", surface.Target)
	}
	if surface.ASTStatus != ASTValid {
		t.Fatalf("ast status = %q, want valid", surface.ASTStatus)
	}
	// cancel is always present on $prompt.
	if !surface.Has(ProposalCancel) {
		t.Fatal("$prompt must always offer ProposalCancel")
	}
}

// TestBuildDecisionSurface_HotForbidsScopeExpansion asserts the $hot strict
// local target boundary: ProposalExpandScope is NEVER generated under any
// condition, even when dependencies are unresolved.
func TestBuildDecisionSurface_HotForbidsScopeExpansion(t *testing.T) {
	cases := []struct {
		name  string
		eval  PreflightEvaluation
		extra string
	}{
		{
			name: "unresolved deps",
			eval: PreflightEvaluation{
				Target:           "index.html",
				ASTStatus:        ASTValid,
				DependencyStatus: DependenciesUnresolved,
				BudgetStatus:     BudgetWithinLimits,
			},
		},
		{
			name: "corrupt ast",
			eval: PreflightEvaluation{
				Target:           "index.html",
				ASTStatus:        ASTCorrupt,
				DependencyStatus: DependenciesResolved,
				BudgetStatus:     BudgetWithinLimits,
			},
		},
		{
			name: "budget exceeded",
			eval: PreflightEvaluation{
				Target:           "index.html",
				ASTStatus:        ASTValid,
				DependencyStatus: DependenciesResolved,
				BudgetStatus:     BudgetExceeded,
			},
		},
		{
			name: "everything broken",
			eval: PreflightEvaluation{
				Target:           "index.html",
				ASTStatus:        ASTCorrupt,
				DependencyStatus: DependenciesUnresolved,
				BudgetStatus:     BudgetExceeded,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			surface := BuildDecisionSurface(tc.eval, "$hot")
			if surface.Has(ProposalExpandScope) {
				t.Fatal("$hot MUST NEVER offer ProposalExpandScope")
			}
			if !surface.Has(ProposalCancel) {
				t.Fatal("$hot must always offer ProposalCancel (fail-fast)")
			}
		})
	}
}

// TestBuildDecisionSurface_HotUnresolvedOffersLocalOnly asserts the $hot
// unresolved-dependency policy: only local-only proposals (inline deps within
// the file) or fail-fast (cancel) are offered — never scope expansion.
func TestBuildDecisionSurface_HotUnresolvedOffersLocalOnly(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTValid,
		DependencyStatus: DependenciesUnresolved,
		BudgetStatus:     BudgetWithinLimits,
	}
	surface := BuildDecisionSurface(eval, "$hot")

	if !surface.Has(ProposalInlineDeps) {
		t.Fatal("$hot must offer local-only ProposalInlineDeps on unresolved deps")
	}
	if surface.Has(ProposalExpandScope) {
		t.Fatal("$hot must NEVER offer ProposalExpandScope")
	}
	if surface.Has(ProposalRepairFirst) {
		t.Fatal("$hot with a valid AST must not offer repair_first")
	}
}

// TestBuildDecisionSurface_HotCorruptOffersRepairOrFailFast asserts $hot on a
// corrupt AST offers repair-first or fail-fast (cancel) — never expansion.
func TestBuildDecisionSurface_HotCorruptOffersRepairOrFailFast(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTCorrupt,
		DependencyStatus: DependenciesResolved,
		BudgetStatus:     BudgetWithinLimits,
	}
	surface := BuildDecisionSurface(eval, "$hot")

	if !surface.Has(ProposalRepairFirst) {
		t.Fatal("$hot must offer ProposalRepairFirst on a corrupt AST")
	}
	if !surface.Has(ProposalCancel) {
		t.Fatal("$hot must offer ProposalCancel (fail-fast) on a corrupt AST")
	}
	if surface.Has(ProposalExpandScope) {
		t.Fatal("$hot must NEVER offer ProposalExpandScope on a corrupt AST")
	}
}

// TestBuildDecisionSurface_PromptCorruptAddsRepair asserts $prompt also offers
// repair-first on a corrupt AST in addition to the systemic options.
func TestBuildDecisionSurface_PromptCorruptAddsRepair(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTCorrupt,
		DependencyStatus: DependenciesResolved,
		BudgetStatus:     BudgetWithinLimits,
	}
	surface := BuildDecisionSurface(eval, "$prompt")

	if !surface.Has(ProposalRepairFirst) {
		t.Fatal("$prompt must offer ProposalRepairFirst on a corrupt AST")
	}
	if !surface.Has(ProposalCancel) {
		t.Fatal("$prompt must always offer ProposalCancel")
	}
	// With no unresolved deps, no scope expansion is offered (it is gated on
	// the unresolved dependency condition).
	if surface.Has(ProposalExpandScope) {
		t.Fatal("$prompt must not offer expand_scope without unresolved deps")
	}
}

// TestBuildDecisionSurface_UnknownSubcommandIsConservative asserts the fallback
// policy for an unrecognized subcommand: a closed surface with only repair and
// cancel — never scope expansion.
func TestBuildDecisionSurface_UnknownSubcommandIsConservative(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTCorrupt,
		DependencyStatus: DependenciesUnresolved,
		BudgetStatus:     BudgetExceeded,
	}
	surface := BuildDecisionSurface(eval, "/build")

	if surface.Has(ProposalExpandScope) {
		t.Fatal("unknown subcommand must never offer ProposalExpandScope")
	}
	if !surface.Has(ProposalCancel) {
		t.Fatal("unknown subcommand must always offer ProposalCancel")
	}
}

// TestProposalIntent_ValidAndIsCancel pins the closed vocabulary.
func TestProposalIntent_ValidAndIsCancel(t *testing.T) {
	valid := []ProposalIntent{
		ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalCancel,
	}
	for _, intent := range valid {
		if !intent.Valid() {
			t.Fatalf("intent %q must be valid", intent)
		}
	}
	if !ProposalCancel.IsCancel() {
		t.Fatal("ProposalCancel must be the cancel intent")
	}
	if ProposalExpandScope.IsCancel() {
		t.Fatal("ProposalExpandScope must not be a cancel intent")
	}
	if ProposalIntent("bogus").Valid() {
		t.Fatal("unknown intent must be invalid")
	}
}

// TestDecisionSurface_IsPureData asserts the surface never carries a functional
// callback: every option is plain presentation data addressable by intent.
func TestDecisionSurface_IsPureData(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTCorrupt,
		DependencyStatus: DependenciesUnresolved,
		BudgetStatus:     BudgetWithinLimits,
	}
	surface := BuildDecisionSurface(eval, "$prompt")
	if len(surface.Options) == 0 {
		t.Fatal("surface must not be empty")
	}
	for i, opt := range surface.Options {
		if opt.ID == "" {
			t.Fatalf("option %d has empty ID", i)
		}
		if opt.Label == "" {
			t.Fatalf("option %d has empty label", i)
		}
		if !opt.Intent.Valid() {
			t.Fatalf("option %d has invalid intent %q", i, opt.Intent)
		}
	}
}

// TestBuildDecisionSurface_HotForbidsScopeExpansionUnderZeroFindings pins that
// even a fully clean $hot surface never offers scope expansion.
func TestBuildDecisionSurface_HotCleanNeverExpands(t *testing.T) {
	eval := PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        ASTValid,
		DependencyStatus: DependenciesResolved,
		BudgetStatus:     BudgetWithinLimits,
	}
	surface := BuildDecisionSurface(eval, "$hot")
	if surface.Has(ProposalExpandScope) {
		t.Fatal("$hot clean surface must never offer ProposalExpandScope")
	}
	if !surface.Has(ProposalCancel) {
		t.Fatal("$hot clean surface must still offer ProposalCancel")
	}
}
