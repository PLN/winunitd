// Package headless is the test-only harness for issue #266: local-account S4U
// linger hosting a long-running background workload. It holds the case
// matrix (the #254 recovery groups, B01-B04 and H01-H22), result records
// bound to an admitted run, the fail-closed summary, the workload fixture's
// probes and the SYSTEM qualification pipe's caller decision.
//
// tests/native/headless-workload wraps Main as a standalone executable. It is
// not a release payload, adds no product IPC method and exports no handle.
// The qualification pipe proves an account and a held caller incarnation
// only; it says nothing about a unit, definition or launch.
package headless
