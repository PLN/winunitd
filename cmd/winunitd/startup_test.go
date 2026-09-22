package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/manager"
)

func TestNoteStartupFailureWritesRedactedDaemonLog(t *testing.T) {
	root := t.TempDir()
	noteStartupFailure(root, nil, errors.New("listen: pipe busy"))
	noteStartupFailure(root, nil, context.Canceled)
	noteStartupFailure(root, nil, errors.New("rejected Environment=TOKEN=1"))
	body, err := os.ReadFile(journal.DaemonLogPath(root))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, journal.DaemonEventStartupFailed) != 2 {
		t.Fatalf("startup records = %d\n%s", strings.Count(text, journal.DaemonEventStartupFailed), text)
	}
	if !strings.Contains(text, "pipe busy") {
		t.Fatalf("reason missing: %s", text)
	}
	if strings.Contains(text, "Environment=") || strings.Contains(text, "context canceled") {
		t.Fatalf("forbidden or cancellation text persisted: %s", text)
	}
}

func TestNoteStartupFailureUsesOpenManagerLog(t *testing.T) {
	root := t.TempDir()
	var seen []journal.DaemonEventView
	m, err := manager.New(manager.Config{
		BaseDir: root,
		DaemonEventEmit: func(view journal.DaemonEventView) {
			seen = append(seen, view)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	noteStartupFailure(root, m, errors.New("listen: unavailable"))
	noteStartupFailure(root, m, context.Canceled)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(journal.DaemonLogPath(root))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, journal.DaemonEventStartupFailed) || !strings.Contains(text, "unavailable") {
		t.Fatalf("manager log missing startup failure: %s", text)
	}
	if strings.Contains(text, "Environment=") {
		t.Fatalf("environment assignment persisted: %s", text)
	}
	found := false
	for _, view := range seen {
		if view.Code == journal.DaemonEventStartupFailed && view.Reason == "listen: unavailable" {
			found = true
		}
		if strings.Contains(view.Reason, "Environment=") {
			t.Fatalf("emitter saw a secret: %+v", view)
		}
	}
	if !found {
		t.Fatalf("emitter missed startup failure: %+v", seen)
	}
}
