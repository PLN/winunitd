package runtime

import (
	"testing"

	"github.com/PLN/winunitd/internal/unit"
)

func TestJobLimitsFromSpec(t *testing.T) {
	t.Parallel()
	if JobLimitsFromSpec(nil) != (JobLimits{}) {
		t.Fatal("nil spec")
	}
	empty := &unit.ServiceSpec{}
	if JobLimitsFromSpec(empty) != (JobLimits{}) {
		t.Fatal("omitted limits")
	}
	svc := &unit.ServiceSpec{
		MemoryMax:        2 << 30,
		MemoryMaxSet:     true,
		ProcessLimit:     8,
		ProcessLimitSet:  true,
		PriorityClass:    unit.PriorityBelowNormal,
		PriorityClassSet: true,
	}
	got := JobLimitsFromSpec(svc)
	want := JobLimits{MemoryMax: 2 << 30, ProcessLimit: 8, PriorityClass: PriorityBelowNormal}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
