//go:build windows

package manager

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

func TestWindowsNativeProxyDisappearance(t *testing.T) {
	for _, kind := range []string{"scm", "scheduled-task"} {
		t.Run(kind, func(t *testing.T) {
			name := ""
			var stop func() error
			directive := "ServiceName"
			if kind == "scm" {
				name = installManagerThrowawaySCM(t)
				stop = func() error {
					_, err := runtime.DefaultSCM().Stop(context.Background(), name, 15*time.Second)
					return err
				}
			} else {
				name = installNativeProbeTask(t)
				directive = "TaskName"
				stop = func() error {
					_, err := runtime.DefaultTaskScheduler().Stop(context.Background(), name, 15*time.Second)
					return err
				}
			}
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "units"), 0755); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			writeUnit(t, filepath.Join(dir, "units"), "peer.service", fmt.Sprintf("[Service]\nType=%s\n%s=%s\nTimeoutStartSec=15s\nTimeoutStopSec=15s\n", kind, directive, name))
			writeUnit(t, filepath.Join(dir, "units"), "bound.service", fmt.Sprintf("[Unit]\nBindsTo=peer.service\nAfter=peer.service\n[Service]\nExecStart=%s\nExecStartArg=%ssleep\n", exe, winunitdHelperArgPrefix))
			m, err := New(Config{BaseDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { stopAll(m) })
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			for cycle := 0; cycle < 3; cycle++ {
				if _, err := m.Start(context.Background(), "bound"); err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				p := m.units["bound.service"].proc
				m.mu.Unlock()
				if p == nil || !p.Alive() {
					t.Fatal("bound process was not alive")
				}
				started := time.Now()
				if err := stop(); err != nil {
					t.Fatal(err)
				}
				waitCond(t, func() bool {
					m.mu.Lock()
					defer m.mu.Unlock()
					return m.units["bound.service"].state == core.Inactive && m.units["bound.service"].proc == nil && m.boundStopsDone == nil
				})
				if p.Alive() {
					t.Fatal("dependent survived confirmed external stop")
				}
				t.Logf("cycle=%d external stop and dependent cleanup=%s", cycle+1, time.Since(started))
			}
		})
	}
}

// A SYSTEM qualification has no interactive-token task session. Register its
// unique test task under that same service account; other runs use the existing
// interactive fixture. Neither path modifies a real workload task.
func installNativeProbeTask(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return installManagerThrowawayTask(t)
	}
	folder := "WinUnitdProxyQualification"
	name := fmt.Sprintf("wu-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	full := `\` + folder + `\` + name
	exe, args := runtime.ThrowawayKeepAliveExec()
	var command, arguments strings.Builder
	if err := xml.EscapeText(&command, []byte(exe)); err != nil {
		t.Fatal(err)
	}
	if err := xml.EscapeText(&arguments, []byte(args)); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Principals><Principal id="system"><UserId>S-1-5-18</UserId><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Settings><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>true</AllowHardTerminate><AllowStartOnDemand>true</AllowStartOnDemand><ExecutionTimeLimit>PT0S</ExecutionTimeLimit></Settings><Actions Context="system"><Exec><Command>%s</Command><Arguments>%s</Arguments></Exec></Actions></Task>`, command.String(), arguments.String())
	path := filepath.Join(t.TempDir(), "task.xml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("schtasks.exe", "/Create", "/TN", full, "/XML", path, "/RU", "SYSTEM").Run(); err != nil {
		t.Fatalf("register SYSTEM fixture task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runtime.DefaultTaskScheduler().Stop(context.Background(), full, 15*time.Second)
		if err := runtime.DeleteThrowawayTask(folder, name); err != nil {
			t.Errorf("delete SYSTEM fixture task: %v", err)
		}
	})
	return full
}
