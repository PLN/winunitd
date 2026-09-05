package notify

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseAndFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want Message
	}{
		{"READY=1\n", Message{Ready: true}},
		{"WATCHDOG=1\n", Message{Watchdog: true}},
		{"STATUS=Processing queue\n", Message{Status: "Processing queue"}},
		{"MAINPID=1234\n", Message{MainPID: 1234, HasPID: true}},
		{"READY=1\nSTATUS=Connected\nWATCHDOG=1\n", Message{Ready: true, Watchdog: true, Status: "Connected"}},
		{"READY=1\r\nFOO=bar\nWATCHDOG=1\n", Message{Ready: true, Watchdog: true}},
		{"UNKNOWN=yes\n", Message{}},
		{"READY=0\n", Message{}},
		{"", Message{}},
	}
	for _, tt := range tests {
		got := Parse(tt.in)
		if got != tt.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}

	out := Format(Message{Ready: true, Watchdog: true, Status: "Waiting for work"})
	if !strings.Contains(out, "READY=1\n") || !strings.Contains(out, "WATCHDOG=1\n") || !strings.Contains(out, "STATUS=Waiting for work\n") {
		t.Fatalf("Format = %q", out)
	}
	round := Parse(out)
	if !round.Ready || !round.Watchdog || round.Status != "Waiting for work" {
		t.Fatalf("round-trip = %+v", round)
	}
}

func TestPipeName(t *testing.T) {
	t.Parallel()
	got := PipeName("foo.service")
	want := `\\.\pipe\winunitd\notify\foo.service`
	if got != want {
		t.Fatalf("PipeName = %q, want %q", got, want)
	}
	if strings.Contains(PipeName(`foo\bar`), `\pipe\winunitd\notify\foo\bar`) {
		t.Fatal("unit id must not inject extra path separators")
	}
}

func TestInjectEnv(t *testing.T) {
	t.Parallel()
	env := Inject([]string{"FOO=bar"}, `\\.\pipe\winunitd\notify\foo.service`, 30*time.Second)
	pipe, ok := LookupEnv(env, EnvNotifyPipe)
	if !ok || pipe != `\\.\pipe\winunitd\notify\foo.service` {
		t.Fatalf("pipe = %q ok=%v env=%v", pipe, ok, env)
	}
	usec, ok := LookupEnv(env, EnvWatchdogUsec)
	if !ok || usec != "30000000" {
		t.Fatalf("usec = %q ok=%v", usec, ok)
	}
}

func TestFakeListenerRoundTrip(t *testing.T) {
	t.Parallel()
	lis, err := ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	got := make(chan Message, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go ServeAccept(ctx, lis, nil, func(m Message) { got <- m })

	if err := Send(ctx, lis.Addr(), Message{Ready: true, Status: "up"}); err != nil {
		t.Fatal(err)
	}
	if err := Send(ctx, lis.Addr(), Message{Watchdog: true}); err != nil {
		t.Fatal(err)
	}

	var sawReady, sawWD bool
	var status string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case m := <-got:
			if m.Ready {
				sawReady = true
			}
			if m.Status != "" {
				status = m.Status
			}
			if m.Watchdog {
				sawWD = true
			}
			if sawReady && sawWD && status == "up" {
				return
			}
		case <-ctx.Done():
			t.Fatalf("ready=%v status=%q watchdog=%v", sawReady, status, sawWD)
		}
	}
	t.Fatalf("ready=%v status=%q watchdog=%v", sawReady, status, sawWD)
}

func TestServeAcceptCancellationClosesIdleClient(t *testing.T) {
	lis, err := ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ServeAccept(ctx, lis, func(int) bool { close(accepted); return true }, func(Message) {})
	}()
	conn, err := Dial(ctx, lis.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("client not accepted")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notification shutdown blocked by idle client")
	}
}

func TestSendWaitsForAcceptance(t *testing.T) {
	lis, err := ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Send(ctx, lis.Addr(), Message{Ready: true}) }()
	conn, err := lis.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	select {
	case err := <-done:
		t.Fatalf("sender returned before server acceptance: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := io.WriteString(conn, acceptanceBanner); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "READY=1\n" {
		t.Fatalf("payload = %q", body)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSendCancellationWithoutAcceptance(t *testing.T) {
	lis, err := ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Send(ctx, lis.Addr(), Message{Ready: true}) }()
	conn, err := lis.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled send succeeded without acceptance")
		}
	case <-time.After(time.Second):
		t.Fatal("send ignored cancellation while waiting for acceptance")
	}
}
