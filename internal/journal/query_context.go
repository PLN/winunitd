package journal

import (
	"context"
	"errors"
	"time"
)

const queryWorkers = 4
const defaultQueryTimeout = 5 * time.Second

// ErrQueryBusy means every bounded query worker is still occupied.
var ErrQueryBusy = errors.New("journal query capacity exhausted")

type queryResult struct {
	entries []Entry
	cursor  string
	more    bool
	err     error
}

// QueryPageContext bounds caller waiting and admits at most four simultaneous
// storage scans. A timed-out native operation retains its slot until it returns.
func (s *Store) QueryPageContext(ctx context.Context, unit string, since time.Time, cursor string, maxBytes int) ([]Entry, string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, cursor, false, err
	}
	if s == nil {
		return nil, cursor, false, nil
	}
	select {
	case s.querySlots <- struct{}{}:
	default:
		return nil, cursor, false, ErrQueryBusy
	}
	result := make(chan queryResult, 1)
	go func() {
		defer func() { <-s.querySlots }()
		entries, next, more, err := s.queryPage(ctx, unit, since, cursor, maxBytes)
		result <- queryResult{entries, next, more, err}
	}()
	select {
	case <-ctx.Done():
		return nil, cursor, false, ctx.Err()
	case got := <-result:
		if err := ctx.Err(); err != nil {
			return nil, cursor, false, err
		}
		return got.entries, got.cursor, got.more, got.err
	}
}
