package manager

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/runtime"
)

// pidNotifyListener hands out in-memory clients that report a chosen PID, as
// the Windows named-pipe listener does. It is not a test listener without
// PIDs, so a client PID of 0 stands for a failed lookup.
type pidNotifyListener struct {
	conns  chan notify.Conn
	closed chan struct{}
	once   sync.Once
}

func newPIDNotifyListener() *pidNotifyListener {
	return &pidNotifyListener{conns: make(chan notify.Conn), closed: make(chan struct{})}
}

func (l *pidNotifyListener) Addr() string { return "pid-notify-test" }

func (l *pidNotifyListener) Accept() (notify.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pidNotifyListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

type pidNotifyConn struct {
	net.Conn
	pid int
}

func (c pidNotifyConn) ClientPID() int { return c.pid }

// send connects as pid and reports whether the server admitted the client
// (sent its acceptance banner). An admitted client then delivers msg.
func (l *pidNotifyListener) send(t *testing.T, pid int, msg notify.Message) bool {
	t.Helper()
	server, client := net.Pipe()
	defer client.Close()
	select {
	case l.conns <- pidNotifyConn{Conn: server, pid: pid}:
	case <-time.After(5 * time.Second):
		t.Fatal("notify listener did not accept")
	}
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := bufio.NewReader(client).ReadString('\n'); err != nil {
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("unexpected rejection error: %v", err)
		}
		return false
	}
	if _, err := io.WriteString(client, notify.Format(msg)); err != nil {
		t.Fatal(err)
	}
	return true
}

// unidentifiedPIDNotifyListener declares that its clients carry no PID, as the
// manager tests' Unix socket listener does.
type unidentifiedPIDNotifyListener struct{ *pidNotifyListener }

func (unidentifiedPIDNotifyListener) notifyUnidentifiedPeersForTest() {}

func openTestNotify(t *testing.T, lis notify.Listener) *notifyRuntime {
	t.Helper()
	m := &Manager{cfg: Config{NotifyListen: func(string) (notify.Listener, error) { return lis, nil }}}
	rt, err := m.openNotify("admission.service", "invocation", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Error(err)
		}
	})
	return rt
}

func assertNotReady(t *testing.T, rt *notifyRuntime) {
	t.Helper()
	select {
	case <-rt.ready:
		t.Fatal("unadmitted client satisfied readiness")
	case <-time.After(50 * time.Millisecond):
	}
}

func waitNotifyReady(t *testing.T, rt *notifyRuntime) {
	t.Helper()
	select {
	case <-rt.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted READY=1 was not delivered")
	}
}

// containsJob reports membership for a fixed PID set; lookups for errPID fail.
type containsJob struct {
	runtime.Job
	pids   map[int]bool
	errPID int
}

func (j containsJob) Contains(pid int) (bool, error) {
	if pid == j.errPID {
		return false, errors.New("job query failed")
	}
	return j.pids[pid], nil
}

// The listener serves before the process exists. A client that connects
// before the main process is assigned cannot be shown to belong to the unit
// and is refused, whatever PID it reports.
func TestNotifyAdmissionRejectsClientsBeforeAssignment(t *testing.T) {
	t.Parallel()
	lis := newPIDNotifyListener()
	rt := openTestNotify(t, lis)
	for _, pid := range []int{4242, 0, -1} {
		if lis.send(t, pid, notify.Message{Ready: true, Watchdog: true}) {
			t.Fatalf("client PID %d admitted before main assignment", pid)
		}
	}
	assertNotReady(t, rt)
	select {
	case <-rt.pulse:
		t.Fatal("unadmitted client delivered WATCHDOG=1")
	default:
	}
	rt.SetMain(4242, nil)
	if !lis.send(t, 4242, notify.Message{Ready: true}) {
		t.Fatal("assigned main process was refused")
	}
	waitNotifyReady(t, rt)
}

// After assignment only the main process and its job tree are admitted. An
// unknown client PID (failed lookup) and a failed job query fail closed.
func TestNotifyAdmissionRejectsUnauthorizedPeers(t *testing.T) {
	t.Parallel()
	lis := newPIDNotifyListener()
	rt := openTestNotify(t, lis)
	rt.SetMain(100, containsJob{pids: map[int]bool{101: true}, errPID: 103})
	for _, pid := range []int{0, -1, 102, 103} {
		if lis.send(t, pid, notify.Message{Ready: true, Status: "forged"}) {
			t.Fatalf("client PID %d admitted outside the unit", pid)
		}
	}
	assertNotReady(t, rt)
	if !lis.send(t, 101, notify.Message{Watchdog: true}) {
		t.Fatal("job member was refused")
	}
	select {
	case <-rt.pulse:
	case <-time.After(5 * time.Second):
		t.Fatal("job member WATCHDOG=1 was not delivered")
	}
	if !lis.send(t, 100, notify.Message{Ready: true}) {
		t.Fatal("main process was refused")
	}
	waitNotifyReady(t, rt)
	rt.mu.Lock()
	status := rt.status
	rt.mu.Unlock()
	if status == "forged" {
		t.Fatal("refused client set STATUS")
	}
}

// Only a manager test listener can admit clients without a PID, and only once
// the main process is assigned.
func TestNotifyAdmissionUnidentifiedPeersNeedTestListener(t *testing.T) {
	t.Parallel()
	lis := unidentifiedPIDNotifyListener{newPIDNotifyListener()}
	rt := openTestNotify(t, lis)
	if lis.send(t, 0, notify.Message{Ready: true}) {
		t.Fatal("client without PID admitted before main assignment")
	}
	assertNotReady(t, rt)
	rt.SetMain(4242, nil)
	if !lis.send(t, 0, notify.Message{Ready: true}) {
		t.Fatal("test listener client refused after assignment")
	}
	waitNotifyReady(t, rt)
}

// earlyNotifyLauncher acts as a workload that sends READY=1 as soon as it
// runs, before Start returns. With assign, the launcher reports the process
// and job first (StartSpec.Assigned), as the Windows launcher does before
// resuming the process; without it the notification precedes assignment.
type earlyNotifyLauncher struct {
	*fakeLauncher
	assign bool
	sent   chan error
}

func (l *earlyNotifyLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if !l.assign {
		spec.Assigned = nil
	}
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	addr, _ := notify.LookupEnv(spec.Env, notify.EnvNotifyPipe)
	sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l.sent <- notify.Send(sendCtx, addr, notify.Message{Ready: true})
	return p, nil
}

const earlyNotifyUnit = `
[Service]
Type=notify
ExecStart=C:\App\worker.exe
WorkingDirectory=C:\App
TimeoutStartSec=1s
`

// A legitimate workload may notify immediately: the launcher assigns it to
// its job before it runs, so its first READY=1 is admitted even though Start
// has not returned yet.
func TestNotifyEarlyReadyFromAssignedWorkload(t *testing.T) {
	t.Parallel()
	launch := &earlyNotifyLauncher{fakeLauncher: fakeNotifyLaunch(), assign: true, sent: make(chan error, 1)}
	m := managerWith(t, launch, map[string]string{"worker.service": earlyNotifyUnit})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := <-launch.sent; err != nil {
		t.Fatalf("early READY=1 from the assigned workload: %v", err)
	}
	assertState(t, m, "worker.service", core.Active)
}

// A client that reaches the pipe before the main process is assigned is
// refused, so it cannot satisfy readiness for the unit.
func TestNotifyReadyBeforeAssignmentIsRefused(t *testing.T) {
	t.Parallel()
	launch := &earlyNotifyLauncher{fakeLauncher: fakeNotifyLaunch(), sent: make(chan error, 1)}
	m := managerWith(t, launch, map[string]string{"worker.service": earlyNotifyUnit})
	_, err := m.Start(context.Background(), "worker")
	if sendErr := <-launch.sent; sendErr == nil {
		t.Fatal("READY=1 before main assignment was admitted")
	}
	if err == nil || !strings.Contains(err.Error(), "READY=1") {
		t.Fatalf("Start = %v, want a READY=1 timeout", err)
	}
}
