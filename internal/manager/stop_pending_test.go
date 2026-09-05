package manager

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

type blockedStopProcess struct {
	runtime.Process
	calls   atomic.Int32
	release chan struct{}
}

func (p *blockedStopProcess) Stop(timeout time.Duration) error {
	p.calls.Add(1)
	<-p.release
	return p.Process.Stop(timeout)
}

func TestStopRetryJoinsPendingAdapterCall(t *testing.T) {
	m := &Manager{clk: timers.DefaultClock()}
	p := &blockedStopProcess{Process: &fakeProc{done: make(chan struct{})}, release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(p.release) }) }
	defer unblock()
	for i := 0; i < 3; i++ {
		if err := m.stopProcess(p, 20*time.Millisecond); err == nil {
			t.Fatal("blocked stop reported success")
		}
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("concurrent adapter stops = %d, want 1", got)
	}
	unblock()
	if err := m.stopProcess(p, time.Second); err != nil {
		t.Fatal("stop did not recover", err)
	}
	if p.Alive() {
		t.Fatal("recovered stop left process alive")
	}
}
