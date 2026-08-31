package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/notify"
)

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb, osGetenvNone)
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--ready") {
		t.Fatalf("help = %s", errb.String())
	}
}

func TestRunMissingPipe(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--ready"}, &out, &errb, osGetenvNone)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), notify.EnvNotifyPipe) {
		t.Fatalf("stderr=%s", errb.String())
	}
}

func TestRunReadyWatchdogStatusAgainstListener(t *testing.T) {
	t.Parallel()
	lis, err := notify.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	got := make(chan notify.Message, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go notify.ServeAccept(ctx, lis, nil, func(m notify.Message) { got <- m })

	getenv := func(k string) string {
		if k == notify.EnvNotifyPipe {
			return lis.Addr()
		}
		return ""
	}

	var out, errb bytes.Buffer
	if code := run([]string{"--ready", "--status", "Waiting for work"}, &out, &errb, getenv); code != 0 {
		t.Fatalf("ready exit %d stderr=%s", code, errb.String())
	}
	if code := run([]string{"--watchdog"}, &out, &errb, getenv); code != 0 {
		t.Fatalf("watchdog exit %d stderr=%s", code, errb.String())
	}

	var sawReady, sawWD bool
	var status string
	for i := 0; i < 3; i++ {
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
			if sawReady && sawWD && status == "Waiting for work" {
				return
			}
		case <-ctx.Done():
			t.Fatalf("ready=%v watchdog=%v status=%q", sawReady, sawWD, status)
		}
	}
	if !sawReady || !sawWD || status != "Waiting for work" {
		t.Fatalf("ready=%v watchdog=%v status=%q", sawReady, sawWD, status)
	}
}

func osGetenvNone(string) string { return "" }
