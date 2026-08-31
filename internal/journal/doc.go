// Package journal stores structured unit logs (DESIGN.md §22).
//
// stdout/stderr from CreateProcess are appended as JSON lines under
// <base-dir>\journal\<unit>.log so they survive daemon-reload. There is
// no database. Follow/tail is not part of this store; Logs returns a
// snapshot of what has been written.
package journal
