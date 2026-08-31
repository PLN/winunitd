package core

import "errors"

// ErrSkipped means a unit was not started because a condition was not
// met (DESIGN.md §15, §38). The unit stays inactive and is not failed.
var ErrSkipped = errors.New("skipped")
