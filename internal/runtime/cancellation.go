package runtime

import "context"

// IsCancellation reports whether every error in a wrapped/joined error tree
// is context.Canceled. Joining cancellation with failed cleanup is a failure.
func IsCancellation(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		seen := false
		for _, child := range children {
			if child == nil {
				continue
			}
			seen = true
			if !IsCancellation(child) {
				return false
			}
		}
		return seen
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return IsCancellation(wrapped.Unwrap())
	}
	return err == context.Canceled
}
