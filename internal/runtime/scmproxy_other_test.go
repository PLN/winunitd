//go:build !windows

package runtime

import (
	"context"
	"testing"
	"time"
)

func TestDefaultSCMDoesNotCallRealSCM(t *testing.T) {
	t.Parallel()
	s := DefaultSCM()
	if s == nil {
		t.Fatal("DefaultSCM is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Names are illustrative only: the stub must not open a real SCM.
	if _, err := s.Start(ctx, "wuauserv", time.Second); err == nil {
		t.Fatal("stub SCM Start must not succeed")
	}
	if _, err := s.Stop(ctx, "eventlog", time.Second); err == nil {
		t.Fatal("stub SCM Stop must not succeed")
	}
	if _, err := s.Query("wuauserv"); err == nil {
		t.Fatal("stub SCM Query must not succeed")
	}
}
