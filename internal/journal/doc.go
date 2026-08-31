// Package journal will store structured unit logs (DESIGN.md §22).
//
// M5 attaches stdout/stderr at CreateProcess. Attach drains those streams
// so the child cannot block on a full pipe; the journal directory and
// status/list output are M8.
package journal
