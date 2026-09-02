//go:build !windows

package runtime

import "testing"

func TestStubJobLimitsPassthrough(t *testing.T) {
	j, err := OpenUnitJob()
	if err != nil {
		t.Fatal(err)
	}
	got, err := j.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.JobMemory != 0 || got.ProcessLimit != 0 || got.PriorityClass != 0 {
		t.Fatalf("no directives: %+v", got)
	}
	if j.ResourceLimitC() != nil || j.ResourceLimitHit() {
		t.Fatal("stub must not report a resource-limit hit")
	}

	j2, err := OpenUnitJobWith(JobLimits{
		MemoryMax:     2 << 30,
		ProcessLimit:  4,
		PriorityClass: PriorityBelowNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = j2.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.JobMemory != 2<<30 || got.ProcessLimit != 4 || got.PriorityClass != PriorityBelowNormal {
		t.Fatalf("OpenUnitJobWith stub = %+v", got)
	}

	j3, err := OpenUnitJobWith(JobLimits{CPUWeight: 5})
	if err != nil {
		t.Fatal(err)
	}
	got, err = j3.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUWeight != 5 || got.CPUControlFlags&(JobCPURateEnable|JobCPURateWeightBased) != JobCPURateEnable|JobCPURateWeightBased {
		t.Fatalf("CPUWeight stub = %+v", got)
	}

	j4, err := OpenUnitJobWith(JobLimits{CPURate: 2500})
	if err != nil {
		t.Fatal(err)
	}
	got, err = j4.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.CPURate != 2500 || got.CPUControlFlags&(JobCPURateEnable|JobCPURateHardCap) != JobCPURateEnable|JobCPURateHardCap {
		t.Fatalf("CPUQuota stub = %+v", got)
	}

	j5, err := OpenUnitJobWith(JobLimits{IoPriority: 1, IoPrioritySet: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err = j5.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if !got.IoPrioritySet || got.IoPriority != 1 {
		t.Fatalf("IoPriority stub = %+v", got)
	}

	if _, err := OpenUnitJobWith(JobLimits{CPUWeight: 1, CPURate: 2500}); err == nil {
		t.Fatal("CPUWeight+CPUQuota stub must fail")
	}
}
