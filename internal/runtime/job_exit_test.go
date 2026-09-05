package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type exitProbeJob struct {
	Job
	probe func() ([]int, error)
}

func (j exitProbeJob) PIDs() ([]int, error) { return j.probe() }

func TestWaitJobEmptyHonorsFailureAndDeadline(t *testing.T) {
	injected := errors.New("injected job query failure")
	if err := waitJobEmpty(context.Background(), exitProbeJob{probe: func() ([]int, error) { return nil, injected }}); !errors.Is(err, injected) {
		t.Fatalf("query failure = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := waitJobEmpty(ctx, exitProbeJob{probe: func() ([]int, error) { return []int{42}, nil }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nonempty job wait = %v", err)
	}
}

func TestWaitJobEmptyWaitsForDescendants(t *testing.T) {
	var empty atomic.Bool
	seen := make(chan struct{}, 1)
	job := exitProbeJob{probe: func() ([]int, error) {
		select {
		case seen <- struct{}{}:
		default:
		}
		if empty.Load() {
			return nil, nil
		}
		return []int{42}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitJobEmpty(ctx, job) }()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal("job not queried")
	}
	select {
	case err := <-done:
		t.Fatalf("nonempty job reported complete: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	empty.Store(true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
