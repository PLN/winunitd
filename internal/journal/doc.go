// Package journal stores structured unit logs (DESIGN.md §22, §24).
//
// stdout/stderr from CreateProcess are appended as JSON lines under
// <base-dir>\journal\<encoded-unit>.log so they survive daemon-reload.
// The file name and per-unit mutex key use the lower-case unit name
// (DESIGN.md §36) with a reversible percent-encoding so Windows-forbidden
// characters cannot collide. Read/Query filter by the record unit field.
//
// The current file is kept open. Lines are buffered and timer-flushed;
// Sync runs on unit exit (Wait), daemon shutdown (Close), and rotate —
// not per line. Query flushes under the per-unit write lock, then scans
// unlocked (capture waits only for that flush). A follow poll skips
// entryID for lines before the cursor timestamp and skips archives whose
// last entry precedes the cursor. A per-unit new-data signal and a byte
// offset in the cursor are later. Current file cap is 10 MiB; 3 rotated
// generations are kept. Follow is a cursor + poll on Query, not a
// streaming RPC.
package journal
