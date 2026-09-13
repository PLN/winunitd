//go:build windows

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestWindowsFormat2PathORRetainsArmedPolicy(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	if err := os.WriteFile(first, nil, 0600); err != nil {
		t.Fatal(err)
	}
	m := startWindowsHelperUnit(t, dir, "ready.service", "Type=simple\n", "sleep", 0, "")
	pathFile := filepath.Join(dir, "units", "ready.path")
	writePolicy := func(directive string) {
		t.Helper()
		body := fmt.Sprintf("[Unit]\nFormatVersion=2\n[Path]\n%s=%s\n%s=%s\n", directive, first, directive, second)
		if err := os.WriteFile(pathFile, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy("PathExists")
	if _, err := m.Start(context.Background(), "ready.path"); err != nil {
		t.Fatal(err)
	}
	p := waitWindowsLiveProc(t, m, "ready.service")
	// Reload changes the next arm, while the live watch retains its OR policy.
	writePolicy("PathExistsAll")
	if _, err := m.Stop("ready.service"); err != nil {
		t.Fatal(err)
	}
	if p.Alive() {
		t.Fatal("initial companion survived stop")
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units["ready.path"]
		return rt != nil && rt.hub != nil && !rt.hub.existsSatisfied
	})
	if err := os.WriteFile(second, nil, 0600); err != nil {
		t.Fatal(err)
	}
	next := waitWindowsLiveProc(t, m, "ready.service")
	if next == p {
		t.Fatal("rising edge did not create a new invocation")
	}
	if _, err := m.Stop("ready.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("ready.service"); err != nil {
		t.Fatal(err)
	}
	if next.Alive() {
		t.Fatal("replacement companion survived stop")
	}
}

func TestWindowsFormat2NativeCPUControls(t *testing.T) {
	for _, tc := range []struct {
		directive    string
		weight, rate uint32
	}{
		{"WindowsCPUWeight=1", 1, 0},
		{"WindowsCPUWeight=5", 5, 0},
		{"WindowsCPUWeight=9", 9, 0},
		{"WindowsCPUQuota=25%", 0, 2500},
		{"WindowsCPUQuota=100%", 0, 10000},
	} {
		t.Run(tc.directive, func(t *testing.T) {
			m := startWindowsHelperUnit(t, t.TempDir(), "cpu.service", fmt.Sprintf("[Unit]\nFormatVersion=2\n[Service]\nType=simple\n%s\n", tc.directive), "sleep", 0, "")
			if _, err := m.Start(context.Background(), "cpu.service"); err != nil {
				t.Fatal(err)
			}
			p := waitWindowsLiveProc(t, m, "cpu.service")
			limits, err := p.Job().QueryLimits()
			if err != nil {
				t.Fatal(err)
			}
			if limits.CPUWeight != tc.weight || limits.CPURate != tc.rate || limits.CPUControlFlags&runtime.JobCPURateEnable == 0 {
				t.Fatalf("native CPU policy mismatch: %+v", limits)
			}
			if tc.weight != 0 && limits.CPUControlFlags&runtime.JobCPURateWeightBased == 0 {
				t.Fatal("missing native weight flag")
			}
			if tc.rate != 0 && limits.CPUControlFlags&runtime.JobCPURateHardCap == 0 {
				t.Fatal("missing native hard-cap flag")
			}
			status, err := m.Status("cpu.service")
			if err != nil || status.Unit.WindowsCPUWeight != tc.weight || status.Unit.WindowsCPUQuota*100 != tc.rate {
				t.Fatalf("status CPU policy mismatch: %+v, %v", status, err)
			}
			if _, err := m.Stop("cpu.service"); err != nil {
				t.Fatal(err)
			}
			if p.Alive() {
				t.Fatal("CPU fixture survived stop")
			}
		})
	}
}
