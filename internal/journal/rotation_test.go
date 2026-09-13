package journal

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRotationFailureRetainsProgressAndCaptureCleanup(t *testing.T) {
	for _, phase := range []string{"close", "remove", ".2", ".1", "current", "open"} {
		t.Run(phase, func(t *testing.T) {
			s := testStore(t)
			s.maxSize, s.flushEvery = 1, time.Hour
			const name = "rotation.service"
			for _, msg := range []string{"one", "two", "three", "four"} {
				if err := s.append(Entry{Unit: name, Message: msg}); err != nil {
					t.Fatal(err)
				}
				if err := s.syncUnit(name); err != nil {
					t.Fatal(err)
				}
			}
			var fail atomic.Bool
			fail.Store(true)
			injected := errors.New("injected archive operation failure")
			s.onClose = func() error {
				if phase == "close" && fail.Load() {
					return injected
				}
				return nil
			}
			s.onRotateRemove = func(path string) error {
				if phase == "remove" && fail.Load() {
					return injected
				}
				return os.Remove(path)
			}
			s.onRotateRename = func(from, to string) error {
				if fail.Load() && ((phase == "current" && from == s.path(name)) || ((phase == ".1" || phase == ".2") && strings.HasSuffix(from, phase))) {
					return injected
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if phase == "open" && fail.Load() && from == s.path(name) {
					return os.Mkdir(from, 0700)
				}
				return nil
			}
			owned := s.fileExisting(name)
			c := s.Attach(name, 42, "rotation-failure", strings.NewReader("rejected\n"), nil)
			for i := 0; i < 3; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				ok := c.WaitContext(ctx)
				cancel()
				if ok || s.fileExisting(name) != owned {
					t.Fatal("failed rotation released cleanup ownership")
				}
			}
			stats := s.CaptureStats(name)
			if stats.DroppedRecords != 1 || stats.StorageErrors == 0 || stats.LastStorageError == "" {
				t.Fatalf("failed rotation accounting: %+v", stats)
			}
			fail.Store(false)
			if phase == "open" {
				if err := os.Remove(s.path(name)); err != nil {
					t.Fatal(err)
				}
			}
			if !c.WaitContext(context.Background()) || s.fileExisting(name) != nil {
				t.Fatal("repaired rotation did not retire")
			}
			// Append one accepted replacement after completing the interrupted
			// rotation. A retry must not delete another older generation.
			if err := s.append(Entry{Unit: name, Message: "five"}); err != nil {
				t.Fatal(err)
			}
			if err := s.syncUnit(name); err != nil {
				t.Fatal(err)
			}
			entries, err := s.Read(name)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			for _, e := range entries {
				messages = append(messages, e.Message)
			}
			if !reflect.DeepEqual(messages, []string{"two", "three", "four", "five"}) {
				t.Fatalf("history changed during failed retries: %v", messages)
			}
		})
	}
}
