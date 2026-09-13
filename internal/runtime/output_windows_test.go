//go:build windows

package runtime

import (
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

func TestStopRetriesProtectedOutputHandleClose(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) { testStopRetriesProtectedOutputHandleClose(t, stream) })
	}
}

func testStopRetriesProtectedOutputHandleClose(t *testing.T, stream string) {
	const protect = 0x00000002
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	output := p.stdout
	if stream == "stderr" {
		output = p.stderr
	}
	h := output.handle
	t.Cleanup(func() {
		_ = p.job.Kill()
		output.mu.Lock()
		retained := output.handle == h
		output.mu.Unlock()
		if retained {
			_ = windows.SetHandleInformation(h, protect, 0)
			_ = output.Close()
		}
	})
	if err := windows.SetHandleInformation(h, protect, protect); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(time.Second); err == nil {
		t.Fatal("protected output close reported success")
	}
	if err := windows.SetHandleInformation(h, protect, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("retry failed", err)
	}
	_, err := windows.GetFileType(h)
	if err == nil {
		t.Fatal("successful stop retry leaked the output handle")
	}
}

func TestOutputCloseRetainsProtectedHandleAcrossPendingRead(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) { testOutputCloseRetainsProtectedHandleAcrossPendingRead(t, stream) })
	}
}

func testOutputCloseRetainsProtectedHandleAcrossPendingRead(t *testing.T, stream string) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	output := p.stdout
	if stream == "stderr" {
		output = p.stderr
	}
	h := output.handle
	t.Cleanup(func() {
		_ = p.job.Kill()
		output.mu.Lock()
		retained := output.handle == h
		output.mu.Unlock()
		if retained {
			_ = windows.SetHandleInformation(h, protectHandleFromClose, 0)
			_ = output.Close()
		}
	})
	if err := windows.SetHandleInformation(h, protectHandleFromClose, protectHandleFromClose); err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { var b [1]byte; _, err := output.Read(b[:]); read <- err }()
	closed := make(chan error, 1)
	go func() { closed <- output.Close() }()
	// Anonymous synchronous pipe reads can remain pending until the last
	// writer exits. Stop must terminate that writer before joining output.
	if err := p.Stop(time.Second); err == nil {
		t.Fatal("process stop discarded protected output ownership")
	}
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("capture close discarded protected output ownership")
		}
	case <-time.After(time.Second):
		t.Fatal("output close did not join its reader after process termination")
	}
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("closed reader reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("output reader survived close")
	}
	if err := windows.SetHandleInformation(h, protectHandleFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("process cleanup could not retry capture's failed close", err)
	}
	if _, err := windows.GetFileType(h); err == nil {
		t.Fatal("capture/process cleanup leaked output handle")
	}
	if err := output.Close(); err != nil {
		t.Fatal("completed output close was not idempotent", err)
	}
}
