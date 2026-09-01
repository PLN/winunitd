// Package unit parses and verifies systemd-like INI unit files.
//
// Unit names are case-insensitive and stored lower-case (DESIGN.md §36).
// The parser is independent of Windows APIs so it can be tested on any GOOS.
package unit
