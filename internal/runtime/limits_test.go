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

	cpu := &unit.ServiceSpec{CPUWeight: 50, CPUWeightSet: true}
	got = JobLimitsFromSpec(cpu)
	if got.CPUWeight != unit.WindowsCPUWeight(50) || got.CPURate != 0 {
		t.Fatalf("CPUWeight spec = %+v", got)
	}
	quota := &unit.ServiceSpec{CPUQuota: 25, CPUQuotaSet: true}
	got = JobLimitsFromSpec(quota)
	if got.CPURate != unit.WindowsCPURate(25) || got.CPUWeight != 0 {
		t.Fatalf("CPUQuota spec = %+v", got)
	}
	io := &unit.ServiceSpec{IoPriority: unit.IoLow, IoPrioritySet: true}
	got = JobLimitsFromSpec(io)
	if !got.IoPrioritySet || got.IoPriority != unit.IoPriorityLowNT {
		t.Fatalf("IoPriority spec = %+v", got)
	}
}
