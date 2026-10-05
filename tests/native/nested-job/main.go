// Command nested-job-fixture is the standalone workload fixture for
// installed-daemon qualification of workload-created nested jobs (#265). It
// is test-only and is never part of a release payload.
package main

import (
	"os"

	"github.com/PLN/winunitd/internal/runtime/runtimetest/nestedjob"
)

func main() {
	os.Exit(nestedjob.Main(nil, os.Args[1:], os.Stdout, os.Stderr))
}
