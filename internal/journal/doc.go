// Package journal stores structured unit logs (DESIGN.md §22, §24).
//
// stdout/stderr from CreateProcess are appended as JSON lines under
// <base-dir>\journal\<unit>.log so they survive daemon-reload. Each
// line carries the unit-start InvocationID so successive runs of the
// same unit can be distinguished. There is no database. Follow/tail is
// not part of this store; Logs returns a snapshot of what has been written.
package journal
