package notify

import (
	"context"
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
