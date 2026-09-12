//go:build windows && race

package journal_test

// Race instrumentation has a separate private-commit allowance; the native
// non-race qualification uses the production allowance below.
const pressurePrivateBudget = 768 << 20
