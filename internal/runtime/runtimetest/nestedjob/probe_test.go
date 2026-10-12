package nestedjob

import "testing"

func TestClassifyProbe(t *testing.T) {
	yes, no := true, false
	denied := ProbeResult{Failure: &Failure{Op: "CreateProcess", Win32: ErrorAccessDenied, Message: "denied"}}
	notFound := ProbeResult{Failure: &Failure{Op: "CreateProcess", Win32: 2, Message: "not found"}}
	unknown := ProbeResult{Failure: &Failure{Op: "CreateProcess", Message: "no code"}}
	created := ProbeResult{Created: true}
	cases := []struct {
		name   string
		expect string
		obs    ProbeObservation
		want   string
	}{
		{"contained denied", ExpectContained, ProbeObservation{Result: denied}, ProbePass},
		{"contained created inside", ExpectContained, ProbeObservation{Result: created, InUnitJob: &yes}, ProbePass},
		{"contained escaped", ExpectContained, ProbeObservation{Result: created, InUnitJob: &no}, ProbeFail},
		{"contained unobserved", ExpectContained, ProbeObservation{Result: created}, ProbeFail},
		{"contained unrelated error", ExpectContained, ProbeObservation{Result: notFound}, ProbeFail},
		{"contained error without code", ExpectContained, ProbeObservation{Result: unknown}, ProbeFail},
		{"contained no failure record", ExpectContained, ProbeObservation{}, ProbeFail},
		{"partial left inner", ExpectPartialBreakaway, ProbeObservation{Result: created, InUnitJob: &yes, InInner: &no}, ProbePass},
		{"partial stayed inner", ExpectPartialBreakaway, ProbeObservation{Result: created, InUnitJob: &yes, InInner: &yes}, ProbeFail},
		{"partial inner unobserved", ExpectPartialBreakaway, ProbeObservation{Result: created, InUnitJob: &yes}, ProbeFail},
		{"partial escaped", ExpectPartialBreakaway, ProbeObservation{Result: created, InUnitJob: &no, InInner: &no}, ProbeFail},
		{"partial denied", ExpectPartialBreakaway, ProbeObservation{Result: denied}, ProbeUnqualified},
		{"partial unrelated error", ExpectPartialBreakaway, ProbeObservation{Result: notFound}, ProbeFail},
		{"silent left inner", ExpectSilentBreakaway, ProbeObservation{Result: created, InUnitJob: &yes, InInner: &no}, ProbePass},
		{"silent stayed inner", ExpectSilentBreakaway, ProbeObservation{Result: created, InUnitJob: &yes, InInner: &yes}, ProbeFail},
		{"silent not created", ExpectSilentBreakaway, ProbeObservation{Result: denied}, ProbeFail},
		{"silent unrelated error", ExpectSilentBreakaway, ProbeObservation{Result: notFound}, ProbeFail},
		{"silent escaped", ExpectSilentBreakaway, ProbeObservation{Result: created, InUnitJob: &no, InInner: &no}, ProbeFail},
		{"unknown expectation", "maybe", ProbeObservation{Result: created, InUnitJob: &yes, InInner: &no}, ProbeFail},
	}
	for _, c := range cases {
		if got, why := ClassifyProbe(c.expect, c.obs); got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}
