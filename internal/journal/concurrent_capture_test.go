package journal

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentCaptureDoesNotWaitForOrReplaceMain(t *testing.T) {
	s := testStore(t)
	main, writer := io.Pipe()
	defer main.Close()
	defer writer.Close()
	s.Attach("work.service", 1, "main", main, nil)
	s.mu.Lock()
	mainGroup := s.capWG["work.service"]
	s.mu.Unlock()
	attached := make(chan *Capture, 1)
	go func() {
		attached <- s.AttachConcurrent("work.service", 2, "stop-helper", strings.NewReader("helper output\n"), nil)
	}()
	var helper *Capture
	select {
	case helper = <-attached:
	case <-time.After(time.Second):
		t.Fatal("helper capture waited for main process EOF")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !helper.WaitContext(ctx) {
		t.Fatal("helper capture did not complete independently")
	}
	s.mu.Lock()
	retained := s.capWG["work.service"] == mainGroup
	s.mu.Unlock()
	if !retained {
		t.Fatal("helper replaced main capture ownership")
	}
	if _, err := io.WriteString(writer, "main output\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if !s.WaitContext(ctx, "work.service") {
		t.Fatal("main capture did not complete")
	}
	entries, err := s.Read("work.service")
	if err != nil || len(entries) != 2 {
		t.Fatalf("combined log: %v %v", entries, err)
	}
	if entries[0].InvocationID != "stop-helper" || entries[1].InvocationID != "main" {
		t.Fatal("capture identities lost")
	}
}

func TestConcurrentCaptureDrainsDuringStorageStall(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var entered, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { entered.Do(func() { close(blocked); <-release }) }
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	helper := s.AttachConcurrent("work.service", 2, "helper", reader, nil)
	produced := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, strings.Repeat("x", invocationQueueBytes*2))
		writer.Close()
		produced <- err
	}()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("writer did not stall")
	}
	select {
	case err := <-produced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled storage blocked helper output drain")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		if helper.WaitContext(ctx) {
			t.Fatal("capture completed while accepted writes remained blocked")
		}
	}
	if s.CaptureStats("work.service").DroppedBytes == 0 {
		t.Fatal("helper queue pressure was not bounded and reported")
	}
	unblock()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !helper.WaitContext(ctx) {
		t.Fatal("capture did not recover after storage resumed")
	}
}
