package nestedjob

import "fmt"

// Probe expectations for the breakaway cases.
const (
	// ExpectContained (N11): no job permits breakaway. The documented
	// result is ERROR_ACCESS_DENIED; a created probe must stay in the unit job.
	ExpectContained = "contained"
	// ExpectPartialBreakaway (N12): the inner job permits explicit
	// breakaway and the unit job does not. The probe leaves only the inner job.
	ExpectPartialBreakaway = "partial-breakaway"
	// ExpectSilentBreakaway (N13): the inner job breaks normal children away
	// silently. The probe must be created, outside the inner job and inside
	// the unit job.
	ExpectSilentBreakaway = "silent-breakaway"
)

// Probe verdicts. Unqualified is a documented-but-unexpected native result
// that leaves the positive control open; it is never a pass.
const (
	ProbePass        = "pass"
	ProbeFail        = "fail"
	ProbeUnqualified = "unqualified"
)

// ErrorAccessDenied is the Win32 code of a refused breakaway.
const ErrorAccessDenied = 5

// ProbeObservation is one probe result with the memberships the owner and
// MAIN observed for a created probe. Nil means not observed.
type ProbeObservation struct {
	Result    ProbeResult
	InUnitJob *bool
	InInner   *bool
}

// ClassifyProbe judges one probe against its expectation. An unrelated
// creation failure, an unobserved membership or an escape from the unit job
// always fails.
func ClassifyProbe(expect string, o ProbeObservation) (string, string) {
	r := o.Result
	if !r.Created {
		win32 := uint32(0)
		if r.Failure != nil {
			win32 = r.Failure.Win32
		}
		switch {
		case expect == ExpectContained && win32 == ErrorAccessDenied:
			return ProbePass, "breakaway denied"
		case expect == ExpectPartialBreakaway && win32 == ErrorAccessDenied:
			return ProbeUnqualified, "breakaway denied where the inner job permits it"
		default:
			return ProbeFail, fmt.Sprintf("creation failed with win32 %d", win32)
		}
	}
	if o.InUnitJob == nil {
		return ProbeFail, "unit-job membership not observed"
	}
	if !*o.InUnitJob {
		return ProbeFail, "escaped the unit job"
	}
	switch expect {
	case ExpectContained:
		return ProbePass, "created inside the unit job"
	case ExpectPartialBreakaway, ExpectSilentBreakaway:
		if o.InInner == nil {
			return ProbeFail, "inner membership not observed"
		}
		if *o.InInner {
			return ProbeFail, "stayed in the inner job although it permits breakaway"
		}
		return ProbePass, "left the inner job, stayed in the unit job"
	}
	return ProbeFail, "unknown expectation " + expect
}
