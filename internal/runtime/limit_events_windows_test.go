//go:build windows

package runtime

import (
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestProcessLimitEventAndCleanup(t *testing.T) {
	job, err := OpenUnitJobWith(JobLimits{ProcessLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := job.Close(); err != nil {
			t.Error(err)
		}
	})
	if job.ResourceLimitHit() {
		t.Fatal("fresh job reports a limit hit")
	}
	first := startSleepHelper(t, nil)
	second := startSleepHelper(t, nil)
	open := func(pid int) windows.Handle {
		h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := windows.CloseHandle(h); err != nil {
				t.Error(err)
			}
		})
		return h
	}
	firstHandle, secondHandle := open(first.Process.Pid), open(second.Process.Pid)
	if err := job.Assign(firstHandle); err != nil {
		t.Fatal(err)
	}
	if err := job.Assign(secondHandle); err == nil {
		t.Fatal("second process admitted beyond ProcessLimit=1")
	}
	select {
	case <-job.ResourceLimitC(): // Actual Windows completion-port notification.
	case <-time.After(5 * time.Second):
		t.Fatal("process-limit notification was not delivered")
	}
	if !job.ResourceLimitHit() {
		t.Fatal("delivered event did not latch ResourceLimitHit")
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	// A rejected assignment does not transfer ownership to this job.
	if err := windows.TerminateProcess(secondHandle, 1); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []windows.Handle{firstHandle, secondHandle} {
		// Wait on retained native handles with a deadline. Cmd.Wait has one
		// cleanup owner, including if this assertion fails.
		status, err := windows.WaitForSingleObject(handle, 5000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("helper cleanup: wait=%d err=%v", status, err)
		}
	}
	if !job.ResourceLimitHit() {
		t.Fatal("closing the job lost the recorded violation")
	}
}

func TestLimitLoopDeliveryAndPortClose(t *testing.T) {
	for _, message := range []uint32{jobMsgActiveProcessLimit, jobMsgProcessMemoryLimit, jobMsgJobMemoryLimit, jobMsgNotificationLimit} {
		t.Run(strconv.FormatUint(uint64(message), 10), func(t *testing.T) {
			port, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			closed := false
			t.Cleanup(func() {
				if !closed {
					_ = windows.CloseHandle(port)
				}
			})
			job := &UnitJob{limitCh: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); job.limitLoop(port) }()
			for _, msg := range []uint32{6, message, message} { // NEW_PROCESS is not a violation; repeated hits are safe.
				if err := windows.PostQueuedCompletionStatus(port, msg, 1, nil); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-job.ResourceLimitC():
			case <-time.After(5 * time.Second):
				t.Fatal("limit loop did not deliver notification")
			}
			if !job.ResourceLimitHit() {
				t.Fatal("notification did not latch hit")
			}
			if err := windows.CloseHandle(port); err != nil {
				t.Fatal(err)
			}
			closed = true
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("limit loop did not exit after port close")
			}
		})
	}
}
