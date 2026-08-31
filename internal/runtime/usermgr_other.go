//go:build !windows

package runtime

import "fmt"

// StartUserManager is only available on Windows (CreateProcessAsUser +
// a WTS or S4U token). Tests inject a launcher; production never falls
// back to a password in a file or environment variable.
func StartUserManager(spec UserManagerSpec) (UserManagerProc, error) {
	if err := validateUserManagerSpec(spec); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: CreateProcessAsUser is only available on Windows", ErrNoUserToken)
}
