package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsCancellationPreservesJoinedFailures(t *testing.T) {
	failure := errors.New("cleanup failed")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"cancel", context.Canceled, true},
		{"wrapped", fmt.Errorf("serve: %w", context.Canceled), true},
		{"joined-cancel", errors.Join(context.Canceled, fmt.Errorf("stop: %w", context.Canceled)), true},
		{"joined-failure", errors.Join(context.Canceled, failure), false},
		{"wrapped-join", fmt.Errorf("serve: %w", errors.Join(context.Canceled, failure)), false},
		{"deadline", errors.Join(context.Canceled, context.DeadlineExceeded), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCancellation(tc.err); got != tc.want {
				t.Fatalf("IsCancellation=%v want=%v", got, tc.want)
			}
		})
	}
}
