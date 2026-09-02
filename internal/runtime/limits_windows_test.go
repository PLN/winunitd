//go:build windows

package runtime

import (
	"testing"
	"unsafe"

	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

func TestNoDirectivesHaveNoExtraJobLimits(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0)
	got, err := p.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 || got.JobMemory != 0 {
		t.Fatalf("memory limit set: %+v", got)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS != 0 || got.ProcessLimit != 0 {
		t.Fatalf("process limit set: %+v", got)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS != 0 || got.PriorityClass != 0 {
		t.Fatalf("priority class set: %+v", got)
	}
	if got.CPUControlFlags&JobCPURateEnable != 0 || got.CPUWeight != 0 || got.CPURate != 0 {
		t.Fatalf("cpu rate set: %+v", got)
	}
	if got.IoPrioritySet {
		t.Fatalf("io priority set: %+v", got)
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

func TestOpenUnitJobWithCPUWeight(t *testing.T) {
	job, err := OpenUnitJobWith(JobLimits{CPUWeight: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	got, err := job.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUControlFlags&JobCPURateEnable == 0 || got.CPUControlFlags&JobCPURateWeightBased == 0 {
		t.Fatalf("CPU weight flags = %#x", got.CPUControlFlags)
	}
	if got.CPUWeight != 5 {
		t.Fatalf("CPUWeight = %d, want 5", got.CPUWeight)
	}
	if got.CPURate != 0 {
		t.Fatalf("CPURate = %d, want 0", got.CPURate)
	}
}

func TestOpenUnitJobWithCPUQuota(t *testing.T) {
	const rate = 2500 // CPUQuota=25%
	job, err := OpenUnitJobWith(JobLimits{CPURate: rate})
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	got, err := job.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUControlFlags&JobCPURateEnable == 0 || got.CPUControlFlags&JobCPURateHardCap == 0 {
		t.Fatalf("CPU hard-cap flags = %#x", got.CPUControlFlags)
	}
	if got.CPURate != rate {
		t.Fatalf("CPURate = %d, want %d", got.CPURate, rate)
	}
	if got.CPUWeight != 0 {
		t.Fatalf("CPUWeight = %d, want 0", got.CPUWeight)
	}
}

func TestOpenUnitJobWithCPUWeightAndQuotaFails(t *testing.T) {
	_, err := OpenUnitJobWith(JobLimits{CPUWeight: 1, CPURate: 2500})
	if err == nil {
		t.Fatal("CPUWeight+CPUQuota must fail")
	}
}

func TestIoPriorityLowMatchesProcess(t *testing.T) {
	p := startHelperLimits(t, "sleep", unit.TypeSimple, 0, JobLimits{
		IoPriority:    unit.IoPriorityLowNT,
		IoPrioritySet: true,
	})
	got, err := p.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if !got.IoPrioritySet || got.IoPriority != unit.IoPriorityLowNT {
		t.Fatalf("recorded IoPriority = %+v", got)
	}
	prio, err := queryProcessIoPriority(p.PID())
	if err != nil {
		t.Fatal(err)
	}
	if prio != unit.IoPriorityLowNT {
		t.Fatalf("process IoPriority = %d, want %d", prio, unit.IoPriorityLowNT)
	}
}

func queryProcessIoPriority(pid int) (uint32, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var prio uint32
	if err := windows.NtQueryInformationProcess(
		h,
		int32(windows.ProcessIoPriority),
		unsafe.Pointer(&prio),
		4,
		nil,
	); err != nil {
		return 0, err
	}
	return prio, nil
}
