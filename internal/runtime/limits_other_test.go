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
}
