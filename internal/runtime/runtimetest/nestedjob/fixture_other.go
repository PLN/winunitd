//go:build !windows

package nestedjob

import "errors"

// Job Objects exist only on Windows; the fixture fails visibly elsewhere.
func run(Invocation, []string) error {
	return errors.New("the nested-job fixture requires Windows")
}
