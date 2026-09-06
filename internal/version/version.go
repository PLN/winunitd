// Package version is the winunitd / winctl release string.
//
// This is a documentation/binary string only (0.1.0-alpha). It is not a
// GitHub release tag and is independent of the control-protocol version.
package version

// Version is printed by winctl --version and winunitd --version.
// Release builds set this with the Go linker's -X flag.
var Version = "0.1.0-alpha"
