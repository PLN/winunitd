//go:build windows

package runtime

import (
	"testing"

	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

func TestNoDirectivesHaveNoExtraJobLimits(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0)
	got, err := p.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	// Windows may fill PriorityClass with NORMAL_PRIORITY_CLASS even when
	// JOB_OBJECT_LIMIT_PRIORITY_CLASS is clear. The flags are the contract.
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 {
		t.Fatalf("memory limit set: %+v", got)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS != 0 {
		t.Fatalf("process limit set: %+v", got)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS != 0 {
		t.Fatalf("priority class set: %+v", got)
	}
	if p.Job().ResourceLimitC() != nil {
		t.Fatal("no MemoryMax/ProcessLimit: ResourceLimitC must be nil")
	}
}

func TestPriorityClassBelowNormalMatchesJob(t *testing.T) {
	p := startHelperLimits(t, "sleep", unit.TypeSimple, 0, JobLimits{
		PriorityClass: PriorityBelowNormal,
	})
	got, err := p.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS == 0 {
		t.Fatal("JOB_OBJECT_LIMIT_PRIORITY_CLASS not set")
	}
	if got.PriorityClass != windows.BELOW_NORMAL_PRIORITY_CLASS {
		t.Fatalf("PriorityClass = %#x, want %#x", got.PriorityClass, windows.BELOW_NORMAL_PRIORITY_CLASS)
	}
}

func TestOpenUnitJobWithProcessLimit(t *testing.T) {
	job, err := OpenUnitJobWith(JobLimits{ProcessLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	got, err := job.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS == 0 {
		t.Fatal("JOB_OBJECT_LIMIT_ACTIVE_PROCESS not set")
	}
	if got.ProcessLimit != 1 {
		t.Fatalf("ProcessLimit = %d", got.ProcessLimit)
	}
	if job.ResourceLimitC() == nil {
		t.Fatal("ProcessLimit must watch violations")
	}
}

func TestOpenUnitJobWithMemoryMax(t *testing.T) {
	const capBytes = 16 << 20
	job, err := OpenUnitJobWith(JobLimits{MemoryMax: capBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	got, err := job.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 {
		t.Fatal("JOB_OBJECT_LIMIT_JOB_MEMORY not set")
	}
	if got.JobMemory != capBytes {
		t.Fatalf("JobMemory = %d, want %d", got.JobMemory, capBytes)
	}
}
