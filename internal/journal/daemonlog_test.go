package journal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonLogStallOverflowAndCleanup(t *testing.T) {
	root := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	log, err := OpenDaemonLog(root, func([]byte) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		_ = log.CloseContext(context.Background())
	})
	log.Record(DaemonEvent{Code: DaemonEventOpen})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach the stalled sink")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < maxDaemonQueueRecords+8; i++ {
			log.Record(DaemonEvent{Code: DaemonEventLifecycleRejected, Unit: "work.service", Reason: "illegal transition"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue waited on the stalled sink")
	}
	stats := log.Stats()
	if stats.DroppedRecords == 0 || stats.DroppedBytes == 0 {
		t.Fatalf("overflow was not counted: %+v", stats)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	started := time.Now()
	err = log.CloseContext(ctx)
	elapsed := time.Since(started)
	cancel()
	if err == nil || elapsed > time.Second {
		t.Fatalf("close blocked on the sink: err=%v elapsed=%s", err, elapsed)
	}
	unblock()
	select {
	case <-log.done:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not release the file after the sink returned")
	}
	if err := os.Remove(log.path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(log.path))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("daemon directory mode = %v err=%v", info, err)
	}
}

func TestDaemonLogRedactsSecretsParserDumpsAndUnknownFields(t *testing.T) {
	root := t.TempDir()
	log, err := OpenDaemonLog(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := "pass" + "word=hunter2"
	uri := "cred" + "man://example/token"
	log.Record(DaemonEvent{Code: DaemonEventLifecycleRejected, Unit: "work.service", Reason: "rejected Environment=TOKEN=1 " + secret})
	log.Record(DaemonEvent{Code: DaemonEventLifecycleRejected, Unit: "work.service", Reason: "parser\n" + uri})
	log.Record(DaemonEvent{Code: DaemonEventLifecycleRejected, Unit: "work.service", Reason: "work.service:12: unknown directive"})
	log.Record(DaemonEvent{Code: "parser.dump", Reason: secret})
	log.RecordFields(map[string]string{
		"code":        DaemonEventLifecycleRejected,
		"unit":        "work.service",
		"reason":      "illegal transition",
		"environment": secret,
		"extra":       uri,
	})
	if err := log.CloseContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, forbidden := range []string{"Environment=", secret, uri, "unknown directive", "parser.dump", "environment", "extra"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("persisted forbidden %q in %s", forbidden, text)
		}
	}
	if !strings.Contains(text, DaemonEventLifecycleRejected) || !strings.Contains(text, "illegal transition") || !strings.Contains(text, "work.service") {
		t.Fatalf("allowlisted event missing: %s", text)
	}
	if bytes.Contains(body, []byte("\n{")) && strings.Count(text, `"extra"`) != 0 {
		t.Fatal("unknown field survived")
	}
	reopened, err := OpenDaemonLog(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.CloseContext(context.Background()) }()
	stats := reopened.Stats()
	if len(stats.Events) == 0 || stats.Events[0].Code == "" {
		t.Fatalf("restart did not reload the bounded tail: %+v", stats.Events)
	}
	for _, ev := range stats.Events {
		if strings.Contains(ev.Reason, secret) || strings.Contains(ev.Unit, secret) || ev.Code == "parser.dump" {
			t.Fatalf("reloaded event kept forbidden data: %+v", ev)
		}
	}
}

func TestDaemonLogRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, DaemonDirName)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDaemonLog(root, nil); err == nil {
		t.Fatal("symlink daemon directory was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("escaped write: entries=%v err=%v", entries, err)
	}
}

func TestRecordDiagnosticOmitsEnvironmentAndParserDumps(t *testing.T) {
	s := testStore(t)
	s.RecordDiagnostic("work.service", "inv", "rejected Environment=TOKEN=1")
	s.RecordDiagnostic("work.service", "inv", "work.service:4: unknown directive\nmore")
	s.RecordDiagnostic("work.service", "inv-ok", "illegal transition: active/running + stop-finished (not applied)")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read("work.service")
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	for _, e := range entries[:2] {
		if e.Message != DaemonEventLifecycleRejected || strings.Contains(e.Message, "Environment=") || strings.Contains(e.Message, "\n") {
			t.Fatalf("redacted diagnostic persisted: %+v", e)
		}
	}
	if !strings.Contains(entries[2].Message, "illegal transition") {
		t.Fatalf("safe diagnostic changed: %+v", entries[2])
	}
}
