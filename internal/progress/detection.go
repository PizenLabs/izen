package progress

// Detection is the detector's verdict ABOUT A LOOP, packaged so a terminal
// result can carry the classification rather than just a class label.
//
// It exists because "the loop stopped" is not the same claim as "the loop made
// no progress", and a reader of a terminal result must be able to tell them
// apart: a bound that ran out after real movement is OBJECTIVE_UNPROVEN, while
// a bound that ran out with the runtime state frozen is NO_PROGRESS. The
// detector holds the evidence for that distinction, so the distinction travels
// with it instead of being re-derived (and mis-derived) by each caller.
type Detection struct {
	// Verdict is the detector's classification of the last observation.
	Verdict Verdict
	// StagnantRounds is how many consecutive identical observations the
	// detector has seen.
	StagnantRounds int
	// Reason is the bounded one-line explanation, empty unless the verdict is
	// VerdictNoProgress.
	Reason string
}

// Detection returns the detector's current verdict as a portable record. It is
// a value copy on purpose: a Detection outliving the detector must keep the
// classification that was true when the loop stopped.
func (d *Detector) Detection() Detection {
	return Detection{
		Verdict:        d.verdict(),
		StagnantRounds: d.stagnant,
		Reason:         d.reason,
	}
}

// verdict returns the classification the last Observe reached, derived rather
// than stored: a run of identical observations shorter than the threshold is
// legitimately UNKNOWN, not PROGRESS and not NO_PROGRESS.
func (d *Detector) verdict() Verdict {
	if d.stagnant >= d.threshold {
		return VerdictNoProgress
	}
	if d.stagnant > 0 {
		return VerdictUnknown
	}
	return VerdictProgress
}
