//go:build windows

package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

func TestWindowsNoDirectivesHaveNoExtraJobLimits(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "plain.service", `
Type=simple
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "plain"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "plain.service")
	got, err := proc.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 {
		t.Fatal("JOB_OBJECT_LIMIT_JOB_MEMORY set without MemoryMax=")
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS != 0 {
		t.Fatal("JOB_OBJECT_LIMIT_ACTIVE_PROCESS set without ProcessLimit=")
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS != 0 {
		t.Fatal("JOB_OBJECT_LIMIT_PRIORITY_CLASS set without PriorityClass=")
	}
	if got.CPUControlFlags&runtime.JobCPURateEnable != 0 {
		t.Fatal("CPU rate control set without CPUWeight=/CPUQuota=")
	}
}

func TestWindowsPriorityClassBelowNormal(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "pri.service", `
Type=simple
PriorityClass=below-normal
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "pri"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "pri.service")
	got, err := proc.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.PriorityClass != windows.BELOW_NORMAL_PRIORITY_CLASS {
		t.Fatalf("PriorityClass = %#x, want %#x", got.PriorityClass, windows.BELOW_NORMAL_PRIORITY_CLASS)
	}
}

func TestWindowsCPUWeightSetsJobRate(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "cpu.service", `
Type=simple
CPUWeight=5000
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "cpu"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "cpu.service")
	got, err := proc.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	want := unit.WindowsCPUWeight(5000)
	if got.CPUWeight != want {
		t.Fatalf("CPUWeight = %d, want %d flags=%#x", got.CPUWeight, want, got.CPUControlFlags)
	}
	if got.CPUControlFlags&runtime.JobCPURateWeightBased == 0 {
		t.Fatal("JOB_OBJECT_CPU_RATE_CONTROL_WEIGHT_BASED not set")
	}
}

func TestWindowsCPUQuotaSetsJobRate(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "quota.service", `
Type=simple
CPUQuota=25%
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "quota"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "quota.service")
	got, err := proc.Job().QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	want := unit.WindowsCPURate(25)
	if got.CPURate != want {
		t.Fatalf("CPURate = %d, want %d flags=%#x", got.CPURate, want, got.CPUControlFlags)
	}
	if got.CPUControlFlags&runtime.JobCPURateHardCap == 0 {
		t.Fatal("JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP not set")
	}
}

func TestWindowsProcessLimitFailsResourceLimit(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "cap.service", `
Type=simple
ProcessLimit=1
`, "spawn-hold", 0, "")
	if _, err := m.Start(context.Background(), "cap"); err != nil {
		t.Fatal(err)
	}
	st := waitWindowsFailedReason(t, m, "cap.service", 8*time.Second)
	if st.Reason != core.ReasonResourceLimit {
		t.Fatalf("reason = %q error=%q state=%s", st.Reason, st.Error, st.ActiveState)
	}
	m.mu.Lock()
	proc := m.procOfLocked("cap.service")
	alive := proc != nil && proc.Alive()
	m.mu.Unlock()
	if alive {
		t.Fatal("unit process still running after resource-limit; daemon must stay up and the unit must fail")
	}
}

func TestWindowsMemoryMaxFailsResourceLimit(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "mem.service", `
Type=simple
MemoryMax=16M
`, "alloc", 0, "")
	if _, err := m.Start(context.Background(), "mem"); err != nil {
		t.Fatal(err)
	}
	st := waitWindowsFailedReason(t, m, "mem.service", 15*time.Second)
	if st.Reason != core.ReasonResourceLimit {
		t.Fatalf("reason = %q error=%q state=%s", st.Reason, st.Error, st.ActiveState)
	}
}

func TestWindowsRestartOnFailureAfterResourceLimit(t *testing.T) {
	dir := t.TempDir()
	count := dir + `\count.txt`
	m := startWindowsHelperUnit(t, dir, "rl.service", `
Type=simple
ProcessLimit=1
Restart=on-failure
RestartSec=200ms
`, "spawn-hold", 0, count)
	if _, err := m.Start(context.Background(), "rl"); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 2, 5*time.Second)
	times := helperCountTimestamps(t, count)
	if len(times) < 2 {
		t.Fatalf("start timestamps = %v", times)
	}
	if gap := times[1].Sub(times[0]); gap < 150*time.Millisecond {
		t.Fatalf("restart gap %s, want >= RestartSec", gap)
	}
}

func waitWindowsFailedReason(t *testing.T, m *Manager, name string, timeout time.Duration) *struct {
	ActiveState string
	Reason      string
	Error       string
} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := m.Status(name)
		if err != nil {
			t.Fatal(err)
		}
		if st.Unit != nil && st.Unit.ActiveState == core.Failed.String() {
			return &struct {
				ActiveState string
				Reason      string
				Error       string
			}{st.Unit.ActiveState, st.Unit.Reason, st.Unit.Error}
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, _ := m.Status(name)
	reason, errstr, state := "", "", ""
	if st != nil && st.Unit != nil {
		reason, errstr, state = st.Unit.Reason, st.Unit.Error, st.Unit.ActiveState
	}
	t.Fatalf("timeout waiting for failed: state=%s reason=%q error=%q", state, reason, errstr)
	return nil
}
