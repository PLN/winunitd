package runtime

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type desktopFixture struct {
	Process
	stdout io.ReadCloser
	stop   func(time.Duration) error
}

type desktopCloser func() error

func (close desktopCloser) Close() error { return close() }

func TestUserDesktopEmptyJobFailureDoesNotAbandonHelper(t *testing.T) {
	failure := errors.New("job close failed")
	closes, stops := 0, 0
	lease := &userDesktopLease{
		pending: desktopCloser(func() error {
			closes++
			if closes == 1 {
				return failure
			}
			return nil
		}),
		helper: &desktopFixture{stop: func(time.Duration) error { stops++; return nil }},
	}
	if err := lease.Close(); !errors.Is(err, failure) {
		t.Fatalf("cleanup: %v", err)
	}
	if lease.pending == nil || lease.helper != nil || stops != 1 {
		t.Fatal("independent cleanup ownership was lost")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.pending != nil || stops != 1 || closes != 2 {
		t.Fatal("cleanup retry repeated completed work")
	}
}

func (p *desktopFixture) Stdout() io.ReadCloser            { return p.stdout }
func (p *desktopFixture) Stop(timeout time.Duration) error { return p.stop(timeout) }

func TestUserDesktopReadinessValidation(t *testing.T) {
	for _, value := range []string{"expected\n", "invalid!\n", "short"} {
		t.Run(strings.TrimSpace(value), func(t *testing.T) {
			reader := io.NopCloser(strings.NewReader(value))
			lease := &userDesktopLease{helper: &desktopFixture{stdout: reader, stop: func(time.Duration) error { return reader.Close() }}}
			err := lease.readReady("expected\n", time.Second)
			if (err == nil) != (value == "expected\n") {
				t.Fatalf("readiness error: %v", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserDesktopTimedOutReadRemainsOwned(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	failure := errors.New("helper stop failed")
	fail := true
	helper := &desktopFixture{stdout: reader, stop: func(time.Duration) error {
		if fail {
			return failure
		}
		return reader.Close()
	}}
	lease := &userDesktopLease{helper: helper}
	if err := lease.readReady("ready\n", time.Millisecond); err == nil {
		t.Fatal("blocked read did not time out")
	}
	if err := lease.Close(); !errors.Is(err, failure) {
		t.Fatalf("stop error: %v", err)
	}
	if lease.helper != helper || lease.readDone == nil {
		t.Fatal("failed cleanup lost ownership")
	}
	fail = false
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.helper != nil || lease.readDone != nil {
		t.Fatal("successful cleanup retained resources")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}
