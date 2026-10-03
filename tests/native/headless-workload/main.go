// Command headless-workload is the standalone background workload fixture
// for issue #266: the long-running S4U workload, its probes, the SYSTEM
// qualification pipe, result records and the summary. It also provisions a
// case: the explicit linger grant and the enabled unit, written through the
// product's own linger store, unit parser and enable writer. It is test
// infrastructure, not a release payload.
package main

import (
	"os"

	"github.com/PLN/winunitd/internal/runtime/runtimetest/headless"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "provision-linger", "provision-unit":
			os.Exit(provision(os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(headless.Main(os.Args[1:], os.Stdout, os.Stderr))
}
